package runner

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

type securityEngineFixture struct {
	engine *Engine
	db     *store.Store
	root   string
	actorA string
	actorB string
}

func newSecurityEngine(t *testing.T) securityEngineFixture {
	t.Helper()
	root := t.TempDir()
	database, err := store.Open(filepath.Join(root, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	actorA := config.AccessHashForEmail("tenant-a@example.test")
	actorB := config.AccessHashForEmail("tenant-b@example.test")
	now := time.Now().UTC()
	catalog := &config.Catalog{
		SchemaVersion: 4,
		Services: map[string]model.ServiceDefinition{
			"svc-a": {Name: "svc-a", ObjectID: "service:svc-a", DisplayName: "Tenant A", TenantID: "tenant-a", Runtime: &model.ComposeServiceRuntime{}},
			"svc-b": {Name: "svc-b", ObjectID: "service:svc-b", DisplayName: "Tenant B", TenantID: "tenant-b", Runtime: &model.ComposeServiceRuntime{}},
		},
		Access: &config.AccessPolicy{
			Enforced: true, DefaultTenant: "tenant-a",
			Tenants: map[string]model.Tenant{
				"default":  {ID: "default", DisplayName: "Default", Status: "active", CreatedAt: now},
				"tenant-a": {ID: "tenant-a", DisplayName: "Tenant A", Status: "active", CreatedAt: now},
				"tenant-b": {ID: "tenant-b", DisplayName: "Tenant B", Status: "active", CreatedAt: now},
			},
			Roles: map[string]model.Role{
				"tenant-reader":     {ID: "tenant-reader", DisplayName: "Tenant reader", Permissions: []model.Permission{model.PermissionRead, model.PermissionInspect}},
				"platform-reader":   {ID: "platform-reader", DisplayName: "Platform reader", Permissions: []model.Permission{model.PermissionRead}},
				"platform-deployer": {ID: "platform-deployer", DisplayName: "Platform deployer", Permissions: []model.Permission{model.PermissionDeploy}},
				"platform-config":   {ID: "platform-config", DisplayName: "Platform config", Permissions: []model.Permission{model.PermissionManageConfig}},
				"platform-admin":    {ID: "platform-admin", DisplayName: "Platform admin", Permissions: []model.Permission{model.Permission("*"), model.PermissionManageAccess}, BuiltIn: true},
			},
			Principals: map[string]config.AccessPrincipal{
				actorA: {Subject: actorA, TenantID: "tenant-a", Roles: []string{"tenant-reader"}},
				actorB: {Subject: actorB, TenantID: "tenant-b", Roles: []string{"tenant-reader"}},
			},
			Bindings: []model.RoleBinding{
				{ID: "tenant-a-kube-read", Subject: actorA, TenantID: "tenant-a", RoleID: "platform-reader", ObjectIDs: []string{"kubernetes"}},
				// This intentionally broad platform binding lets the test reach the
				// tenant boundary. A tenant actor must still not cross it.
				{ID: "tenant-a-kube-deploy", Subject: actorA, TenantID: "tenant-a", RoleID: "platform-deployer", ObjectIDs: []string{"*"}},
				{ID: "tenant-a-extension-manage", Subject: actorA, TenantID: "tenant-a", RoleID: "platform-config", ObjectIDs: []string{"extensions"}},
				{ID: "tenant-a-compose-manage", Subject: actorA, TenantID: "tenant-a", RoleID: "platform-config", ObjectIDs: []string{"service:svc-a"}},
			},
		},
		Fleet: &config.FleetPolicy{Enabled: true, AllowRemoteRunners: true, RequiremTLS: true, HeartbeatTimeoutSeconds: 30},
		Kubernetes: map[string]model.KubernetesTarget{
			"cluster-a": {
				Cluster: "cluster-a", Context: "ctx-a", Namespace: "ns-a", TenantID: "tenant-a",
				Allowlist: []string{"deployment/app-a"}, ResourceKinds: []string{"Deployment"},
			},
			"cluster-b": {
				Cluster: "cluster-b", Context: "ctx-b", Namespace: "ns-b", TenantID: "tenant-b",
				Allowlist: []string{"deployment/app-b"}, ResourceKinds: []string{"Deployment"},
			},
		},
		Terminal: &config.TerminalPolicy{
			Enabled: true, BreakGlass: true, MaxSessionSeconds: 60,
			Commands: map[string]model.TerminalCommand{
				"version": {Name: "version", Executable: "/bin/echo", Arguments: []string{"ok"}, ReadOnly: true, TimeoutSeconds: 10},
			},
		},
		Files:      &config.FilePolicy{Enabled: true, Roots: map[string]string{"managed": filepath.Join(root, "managed")}, MaxFileBytes: 4096},
		Extensions: &config.ExtensionPolicy{Enabled: true, TrustedPublishers: []string{"test-publisher"}, MaxPackageBytes: 4096},
	}
	if err := os.MkdirAll(filepath.Join(root, "managed"), 0o700); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngineChecked(catalog, database, &fakeExecutor{}, root)
	if err != nil {
		t.Fatal(err)
	}
	return securityEngineFixture{engine: engine, db: database, root: root, actorA: actorA, actorB: actorB}
}

func TestComposeHTTPRoutesRejectCrossTenantRevisionIDOR(t *testing.T) {
	fixture := newSecurityEngine(t)
	ctx := context.Background()
	now := time.Now().UTC()
	revision := model.ComposeRevision{
		ID: mustUUID(t), IdempotencyKey: mustUUID(t), Service: "svc-b",
		Digest: "sha256:candidate", ExpectedDigest: "sha256:baseline", Source: "test",
		Content: "services: {}", Validated: true, State: "approved", ActorHash: strings.Repeat("c", 64),
		ConfirmationPhrase: "confirm", TenantID: "tenant-b", ServerID: "server-b",
		ProjectName: "svc-b", PolicyDigest: "sha256:policy", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if _, _, err := fixture.db.SaveComposeRevisionIdempotent(ctx, revision, "sha256:request"); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(fixture.engine, fixture.db)
	requests := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/compose/svc-b", ""},
		{http.MethodPost, "/v1/compose/svc-b/revisions", `{"content":"services: {}","expectedDigest":"sha256:baseline","mode":"propose","idempotencyKey":"11111111-1111-4111-8111-111111111111"}`},
		{http.MethodPost, "/v1/compose/svc-b/revisions/" + revision.ID + "/approve", `{"digest":"sha256:candidate","confirmation":"confirm"}`},
		{http.MethodPost, "/v1/compose/svc-b/revisions/" + revision.ID + "/apply", `{"idempotencyKey":"22222222-2222-4222-8222-222222222222"}`},
		{http.MethodPost, "/v1/compose/svc-b/revisions/" + revision.ID + "/rollback", `{"confirmation":"回滚 Compose 变更 ` + revision.ID + `","idempotencyKey":"33333333-3333-4333-8333-333333333333"}`},
	}
	for _, item := range requests {
		t.Run(item.method+" "+item.path, func(t *testing.T) {
			request := httptest.NewRequest(item.method, item.path, strings.NewReader(item.body))
			request.Header.Set("X-AreaSong-Ops-Actor-Hash", fixture.actorA)
			if item.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestPlatformPermissionDoesNotGrantGlobalResourceIDOR(t *testing.T) {
	fixture := newSecurityEngine(t)
	ctx := context.Background()

	if _, err := fixture.engine.AccessControl(ctx, fixture.actorA); err == nil {
		t.Fatal("tenant role unexpectedly accessed global access policy")
	}
	if err := fixture.engine.RegisterServer(ctx, fixture.actorA, model.ServerNode{
		ID: "server-a", Hostname: "server-a", Environment: "production", State: model.NodeUnknown,
	}); err == nil {
		t.Fatal("tenant role unexpectedly registered a platform fleet node")
	}
	if _, err := fixture.engine.TerminalCommands(ctx, fixture.actorA); err == nil {
		t.Fatal("tenant role unexpectedly listed the restricted terminal")
	}
}

func TestPlatformReaderCanReadGlobalViewsButCannotMutateThem(t *testing.T) {
	fixture := newSecurityEngine(t)
	ctx := context.Background()
	reader := config.AccessHashForEmail("platform-reader@example.test")
	policy, snapshot, err := fixture.engine.effectiveAccessPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	policy.Principals[reader] = config.AccessPrincipal{Subject: reader, TenantID: "tenant-a", Roles: []string{"tenant-reader"}}
	policy.Bindings = append(policy.Bindings,
		model.RoleBinding{ID: "reader-access", Subject: reader, TenantID: "tenant-a", RoleID: "platform-reader", ObjectIDs: []string{"access"}},
		model.RoleBinding{ID: "reader-credentials", Subject: reader, TenantID: "tenant-a", RoleID: "platform-reader", ObjectIDs: []string{"credentials"}},
		model.RoleBinding{ID: "reader-files", Subject: reader, TenantID: "tenant-a", RoleID: "platform-reader", ObjectIDs: []string{"files"}},
	)
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.SaveAccessPolicySnapshot(ctx, model.AccessPolicySnapshot{
		Digest: digestText(string(policyJSON)), PolicyJSON: string(policyJSON), ActorHash: "test",
	}, snapshot.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.engine.AccessControl(ctx, reader); err != nil {
		t.Fatalf("platform reader could not read access policy: %v", err)
	}
	if _, err := fixture.engine.AccessChanges(ctx, reader); err != nil {
		t.Fatalf("platform reader could not list access changes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "managed", "reader.txt"), []byte("read-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.engine.ManagedFile(ctx, reader, "managed", "reader.txt"); err != nil {
		t.Fatalf("platform reader could not read managed file: %v", err)
	}
	if _, err := fixture.engine.UpdateAccess(ctx, reader, model.AccessControlUpdateRequest{
		IdempotencyKey: mustUUID(t),
	}); err == nil {
		t.Fatal("platform reader unexpectedly mutated access policy")
	}
}

func TestKubernetesViewAndOperationsStayWithinActorTenant(t *testing.T) {
	fixture := newSecurityEngine(t)
	ctx := context.Background()
	created := time.Now().UTC()
	for _, operation := range []model.KubernetesOperation{
		{ID: "op-a", IdempotencyKey: "op-key-a", Target: fixture.engine.catalog.Kubernetes["cluster-a"], TenantID: "tenant-a", Action: "validate", State: "succeeded", CreatedAt: created},
		{ID: "op-b", IdempotencyKey: "op-key-b", Target: fixture.engine.catalog.Kubernetes["cluster-b"], TenantID: "tenant-b", Action: "validate", State: "succeeded", CreatedAt: created.Add(time.Second)},
	} {
		if err := fixture.db.SaveKubernetesOperation(ctx, operation, fixture.actorA, "output"); err != nil {
			t.Fatal(err)
		}
	}
	view, err := fixture.engine.KubernetesView(ctx, fixture.actorA)
	if err != nil {
		t.Fatal(err)
	}
	targets, ok := view["targets"].(map[string]model.KubernetesTarget)
	if !ok {
		t.Fatalf("targets type=%T", view["targets"])
	}
	if _, exists := targets["cluster-b"]; exists {
		t.Fatal("tenant A received tenant B Kubernetes target")
	}
	if _, exists := targets["cluster-a"]; !exists {
		t.Fatal("tenant A lost its Kubernetes target")
	}
	operations, ok := view["operations"].([]model.KubernetesOperation)
	if !ok {
		t.Fatalf("operations type=%T", view["operations"])
	}
	if len(operations) != 1 || operations[0].TenantID != "tenant-a" || operations[0].ID != "op-a" {
		t.Fatalf("cross-tenant operations leaked: %+v", operations)
	}
}

func TestKubernetesTenantAuthorizationUsesEffectiveAccessPolicy(t *testing.T) {
	fixture := newSecurityEngine(t)
	ctx := context.Background()
	policy, snapshot, err := fixture.engine.effectiveAccessPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	principal := policy.Principals[fixture.actorA]
	principal.TenantID = "tenant-b"
	policy.Principals[fixture.actorA] = principal
	for index := range policy.Bindings {
		if policy.Bindings[index].Subject == fixture.actorA {
			policy.Bindings[index].TenantID = "tenant-b"
		}
	}
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.SaveAccessPolicySnapshot(ctx, model.AccessPolicySnapshot{
		Digest: digestText(string(policyJSON)), PolicyJSON: string(policyJSON), ActorHash: "test",
	}, snapshot.Version); err != nil {
		t.Fatal(err)
	}

	if err := fixture.engine.authorizeKubernetesTenant(
		ctx, fixture.actorA, fixture.engine.catalog.Kubernetes["cluster-b"],
	); err != nil {
		t.Fatalf("effective tenant was not authorized: %v", err)
	}
	if err := fixture.engine.authorizeKubernetesTenant(
		ctx, fixture.actorA, fixture.engine.catalog.Kubernetes["cluster-a"],
	); err == nil || !strings.Contains(err.Error(), "租户") {
		t.Fatalf("stale catalog tenant remained authorized: %v", err)
	}
}

func TestKubernetesViewReflectsDynamicAdminRevocation(t *testing.T) {
	fixture := newSecurityEngine(t)
	ctx := context.Background()
	principal := fixture.engine.catalog.Access.Principals[fixture.actorA]
	principal.Roles = []string{"platform-admin"}
	fixture.engine.catalog.Access.Principals[fixture.actorA] = principal

	view, err := fixture.engine.KubernetesView(ctx, fixture.actorA)
	if err != nil {
		t.Fatal(err)
	}
	targets, ok := view["targets"].(map[string]model.KubernetesTarget)
	if !ok {
		t.Fatalf("targets type=%T", view["targets"])
	}
	if _, exists := targets["cluster-b"]; exists {
		t.Fatal("stale catalog admin role bypassed the effective policy revocation")
	}
	if _, exists := targets["cluster-a"]; !exists {
		t.Fatal("effective tenant target was unexpectedly hidden")
	}
}

func TestKubernetesTargetPreservesTenantAndRejectsCrossTenantOperation(t *testing.T) {
	fixture := newSecurityEngine(t)
	target, err := fixture.engine.kubernetesTarget(fixture.engine.catalog.Kubernetes["cluster-b"])
	if err != nil {
		t.Fatal(err)
	}
	if target.TenantID != "tenant-b" {
		t.Fatalf("target tenant=%q want tenant-b", target.TenantID)
	}
	manifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: app-b\n  namespace: ns-b\n"
	operation, _, err := fixture.engine.Kubernetes(context.Background(), fixture.actorA, model.KubernetesRequest{
		Target: fixture.engine.catalog.Kubernetes["cluster-b"], Action: "validate", Manifest: manifest, IdempotencyKey: "cross-tenant-kube",
	})
	if err == nil || !strings.Contains(err.Error(), "租户") {
		t.Fatalf("cross-tenant operation=%+v err=%v", operation, err)
	}
}

func TestKubernetesOperationsEndpointRejectsDirectApply(t *testing.T) {
	fixture := newSecurityEngine(t)
	manifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: app-a\n  namespace: ns-a\n"
	payload, err := json.Marshal(model.KubernetesRequest{
		Target: fixture.engine.catalog.Kubernetes["cluster-a"], Action: "apply", Manifest: manifest,
		Confirmation: "应用 Kubernetes 清单", IdempotencyKey: "direct-apply-blocked",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/kubernetes/operations", bytes.NewReader(payload))
	request.Header.Set(actorHeader, fixture.actorA)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewServer(fixture.engine, fixture.db).ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "双人批准计划") {
		t.Fatalf("direct apply status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestOrdinaryActorCannotForgeRunnerHeartbeat(t *testing.T) {
	fixture := newSecurityEngine(t)
	ctx := context.Background()
	if err := fixture.db.UpsertRunnerNode(ctx, model.RunnerNode{
		ID: "runner-a", ServerID: "server-a", Hostname: "runner-a", Version: "v1", State: model.NodeOnline,
	}, "tenant-a"); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(fixture.engine, fixture.db)
	body := bytes.NewBufferString(`{"version":"v2","nonce":"n-1"}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/fleet/runners/runner-a/heartbeat", body)
	request.Header.Set(actorHeader, fixture.actorA)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("forged heartbeat status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/fleet/runners/runner-a/heartbeat", bytes.NewBufferString(`{"runnerId":"runner-a","version":"v2","nonce":"n-2"}`))
	request.Header.Set(actorHeader, fixture.actorA)
	request.Header.Set(runnerIDHeader, "runner-a")
	request.Header.Set("X-AreaSong-Runner-Nonce", "n-2")
	request.Header.Set("X-AreaSong-Runner-Timestamp", time.Now().UTC().Format(time.RFC3339Nano))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("non-mTLS forged heartbeat status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRemoteRunnerListenerRejectsHumanControlPlaneAPI(t *testing.T) {
	next := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	handler := RemoteRunnerHandler(next)
	for _, test := range []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/v1/services", http.StatusNotFound},
		{http.MethodGet, "/v1/runner/fleet-updates", http.StatusNotFound},
		{http.MethodPost, "/v1/runner/fleet-updates", http.StatusNotFound},
		{http.MethodPost, "/v1/fleet/runners/runner-a/heartbeat", http.StatusNoContent},
		{http.MethodPost, "/v1/fleet/runners/runner-a/fleet-updates/claim", http.StatusNoContent},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("%s %s status=%d want=%d", test.method, test.path, response.Code, test.want)
		}
	}
}

func TestRunnerMTLSIdentityRequiresNameAndPinnedFingerprint(t *testing.T) {
	certificate := &x509.Certificate{Raw: []byte("runner-a-certificate")}
	certificate.Subject.CommonName = "runner-a"
	policy := &config.FleetPolicy{Enabled: true, AllowRemoteRunners: true, RequiremTLS: true}
	node := model.RunnerNode{
		ID: "runner-a", CertificateFingerprint: certificateFingerprint(certificate.Raw),
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/fleet/runners/runner-a/heartbeat", nil)
	request.Header.Set(runnerIDHeader, node.ID)
	request.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{certificate},
		VerifiedChains:   [][]*x509.Certificate{{certificate}},
	}
	if err := requireRunnerTransportIdentity(request, policy, node); err != nil {
		t.Fatalf("valid mTLS identity rejected: %v", err)
	}
	wrongName := *certificate
	wrongName.Subject.CommonName = "runner-b"
	request.TLS.PeerCertificates = []*x509.Certificate{&wrongName}
	request.TLS.VerifiedChains = [][]*x509.Certificate{{&wrongName}}
	if err := requireRunnerTransportIdentity(request, policy, node); err == nil || !strings.Contains(err.Error(), "Runner ID") {
		t.Fatalf("wrong certificate name err=%v", err)
	}
	request.TLS.PeerCertificates = []*x509.Certificate{certificate}
	request.TLS.VerifiedChains = [][]*x509.Certificate{{certificate}}
	node.CertificateFingerprint = "sha256:" + strings.Repeat("0", 64)
	if err := requireRunnerTransportIdentity(request, policy, node); err == nil || !strings.Contains(err.Error(), "指纹") {
		t.Fatalf("wrong certificate fingerprint err=%v", err)
	}
}

func TestManagedFileRejectsTraversalAndSymlink(t *testing.T) {
	fixture := newSecurityEngine(t)
	root := fixture.engine.catalog.Files.Roots["managed"]
	outside := filepath.Join(fixture.root, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside.txt", "nested/../../outside.txt", "/etc/passwd", "bad\x00name"} {
		if _, _, _, err := fixture.engine.resolveManagedPath("managed", path); err == nil {
			t.Fatalf("path %q escaped managed root", path)
		}
	}
	if _, _, _, err := fixture.engine.resolveManagedPath("managed", "link.txt"); err == nil {
		t.Fatal("managed file followed a symlink")
	}
}

func TestManagedFileRejectsWritableAllowlistRootAndTarget(t *testing.T) {
	fixture := newSecurityEngine(t)
	root := fixture.engine.catalog.Files.Roots["managed"]
	target := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(target, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o770); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.engine.resolveManagedPath("managed", "inside.txt"); err == nil {
		t.Fatal("group-writable managed root was accepted")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o660); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.engine.resolveManagedPath("managed", "inside.txt"); err == nil {
		t.Fatal("group-writable managed file was accepted")
	}
}

func TestExtensionStagingFailureDoesNotReportStored(t *testing.T) {
	fixture := newSecurityEngine(t)
	content := []byte("#!/bin/sh\necho extension\n")
	digest := digestText(string(content))
	manifest := model.ExtensionManifest{
		Purpose: model.ExtensionManifestPurpose, SchemaVersion: model.ExtensionManifestSchema,
		ID: "demo-ext", Version: "v1", Type: "script", Entrypoint: "main.sh",
		Digest: digest, Publisher: "test-publisher",
	}
	storagePath, err := fixture.engine.extensionStoragePath(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storagePath, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, created, err := fixture.engine.UploadExtension(context.Background(), fixture.actorA, model.ExtensionUploadRequest{
		Manifest: manifest, Content: base64.StdEncoding.EncodeToString(content), IdempotencyKey: mustUUID(t),
	})
	if err == nil || !strings.Contains(err.Error(), "暂存失败") || !created {
		t.Fatalf("staging failure result=%+v created=%v err=%v", result, created, err)
	}
	if result.Stored {
		// The durable row below is the authoritative terminal state.
		t.Fatalf("failed staging was reported as stored: %+v", result)
	}
	packages, err := fixture.db.ListExtensionPackages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 1 || packages[0].Stored || packages[0].State != "failed" {
		t.Fatalf("staging record=%+v", packages)
	}
	if _, err := os.Stat(storagePath); !os.IsNotExist(err) {
		t.Fatalf("failed staging file still exists, stat err=%v", err)
	}
}
