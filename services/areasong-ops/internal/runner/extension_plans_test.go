package runner

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
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

type fakeExtensionRuntime struct {
	calls int
}

func (runtime *fakeExtensionRuntime) Execute(
	context.Context,
	config.ExtensionPolicy,
	model.ExtensionManifest,
	[]byte,
	[]byte,
) (string, int, error) {
	runtime.calls++
	return "extension-ok", 0, nil
}

func TestExtensionPlanRequiresIndependentApprovalAndCreatorExecution(t *testing.T) {
	fixture := newExtensionPlanFixture(t)
	ctx := context.Background()
	content := minimalWASMModule()
	manifest := fixture.manifest(content)
	uploaded, created, err := fixture.engine.UploadExtension(ctx, fixture.actors[0], model.ExtensionUploadRequest{
		Manifest: manifest, Content: base64.StdEncoding.EncodeToString(content), IdempotencyKey: mustUUID(t),
	})
	if err != nil || !created || !uploaded.Stored {
		t.Fatalf("uploaded=%+v created=%v err=%v", uploaded, created, err)
	}
	plan, created, err := fixture.engine.CreateExtensionPlan(ctx, fixture.actors[0], model.ExtensionPlanRequest{
		ExtensionID: manifest.ID, ExtensionVersion: manifest.Version, ObjectID: "service:demo",
		Input: []byte(`{"operation":"inspect"}`), IdempotencyKey: mustUUID(t),
	})
	if err != nil || !created || plan.State != "pending_approval" || plan.PlanDigest == "" {
		t.Fatalf("plan=%+v created=%v err=%v", plan, created, err)
	}
	if _, err := fixture.engine.ExecuteExtensionPlan(ctx, fixture.actors[0], plan.ID, model.ExtensionPlanExecuteRequest{IdempotencyKey: mustUUID(t)}); err == nil {
		t.Fatal("creator executed unapproved extension plan")
	}
	plan, err = fixture.engine.ApproveExtensionPlan(ctx, fixture.actors[1], plan.ID, model.ExtensionPlanApprovalRequest{
		Digest: plan.PlanDigest, Confirmation: plan.ConfirmationPhrase,
	})
	if err != nil || plan.State != "approved" || plan.ApprovalPolicy != model.ApprovalPolicyTwoParty {
		t.Fatalf("independent approval=%+v err=%v", plan, err)
	}
	if replay, err := fixture.engine.ApproveExtensionPlan(ctx, fixture.actors[1], plan.ID, model.ExtensionPlanApprovalRequest{
		Digest: plan.PlanDigest, Confirmation: plan.ConfirmationPhrase,
	}); err != nil || replay.State != "approved" || replay.ApprovedByHash != fixture.actors[1] {
		t.Fatalf("approval replay=%+v err=%v", replay, err)
	}
	if _, err := fixture.engine.ExecuteExtensionPlan(ctx, fixture.actors[1], plan.ID, model.ExtensionPlanExecuteRequest{IdempotencyKey: mustUUID(t)}); err == nil {
		t.Fatal("approver executed extension plan")
	}
	key := mustUUID(t)
	finished, err := fixture.engine.ExecuteExtensionPlan(ctx, fixture.actors[0], plan.ID, model.ExtensionPlanExecuteRequest{IdempotencyKey: key})
	if err != nil || finished.State != "succeeded" || finished.Output != "extension-ok" || fixture.runtime.calls != 1 {
		t.Fatalf("finished=%+v calls=%d err=%v", finished, fixture.runtime.calls, err)
	}
	replayed, err := fixture.engine.ExecuteExtensionPlan(ctx, fixture.actors[0], plan.ID, model.ExtensionPlanExecuteRequest{IdempotencyKey: key})
	if err != nil || replayed.State != "succeeded" || fixture.runtime.calls != 1 {
		t.Fatalf("replayed=%+v calls=%d err=%v", replayed, fixture.runtime.calls, err)
	}
	if _, err := fixture.engine.ExecuteExtensionPlan(ctx, fixture.actors[2], plan.ID, model.ExtensionPlanExecuteRequest{IdempotencyKey: key}); err == nil {
		t.Fatal("a different actor replayed the execution idempotency key")
	}
	if _, err := fixture.engine.ExecuteExtensionPlan(ctx, fixture.actors[0], plan.ID, model.ExtensionPlanExecuteRequest{IdempotencyKey: mustUUID(t)}); err == nil {
		t.Fatal("extension plan accepted a different execution idempotency key")
	}
	audit, err := fixture.database.ListAudit(ctx, 30, 0)
	if err != nil {
		t.Fatal(err)
	}
	events := map[string]bool{}
	for _, entry := range audit {
		if entry.Resource == plan.ID {
			events[entry.Event] = true
		}
	}
	for _, event := range []string{"extension.plan.created", "extension.plan.approved", "extension.plan.executed"} {
		if !events[event] {
			t.Fatalf("missing audit event %s: %+v", event, events)
		}
	}
}

func TestExtensionExecutionRejectsTamperedArtifact(t *testing.T) {
	fixture := newExtensionPlanFixture(t)
	ctx := context.Background()
	content := minimalWASMModule()
	manifest := fixture.manifest(content)
	if _, _, err := fixture.engine.UploadExtension(ctx, fixture.actors[0], model.ExtensionUploadRequest{
		Manifest: manifest, Content: base64.StdEncoding.EncodeToString(content), IdempotencyKey: mustUUID(t),
	}); err != nil {
		t.Fatal(err)
	}
	plan, _, err := fixture.engine.CreateExtensionPlan(ctx, fixture.actors[0], model.ExtensionPlanRequest{
		ExtensionID: manifest.ID, ExtensionVersion: manifest.Version, ObjectID: "service:demo",
		Input: []byte(`{"operation":"inspect"}`), IdempotencyKey: mustUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.engine.ApproveExtensionPlan(ctx, fixture.actors[1], plan.ID, model.ExtensionPlanApprovalRequest{Digest: plan.PlanDigest, Confirmation: plan.ConfirmationPhrase}); err != nil {
		t.Fatal(err)
	}
	_, path, err := fixture.database.GetStoredExtensionPackage(ctx, manifest.ID, manifest.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	finished, err := fixture.engine.ExecuteExtensionPlan(ctx, fixture.actors[0], plan.ID, model.ExtensionPlanExecuteRequest{IdempotencyKey: mustUUID(t)})
	if err == nil || finished.State != "failed" || fixture.runtime.calls != 0 {
		t.Fatalf("tampered execution plan=%+v calls=%d err=%v", finished, fixture.runtime.calls, err)
	}
}

func TestExtensionUploadRejectsSignatureDomainMismatch(t *testing.T) {
	fixture := newExtensionPlanFixture(t)
	content := minimalWASMModule()
	for _, test := range []struct {
		name   string
		mutate func(*model.ExtensionManifest)
	}{
		{name: "purpose", mutate: func(manifest *model.ExtensionManifest) { manifest.Purpose = "areasong-ops.runner-update" }},
		{name: "schema", mutate: func(manifest *model.ExtensionManifest) { manifest.SchemaVersion++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := fixture.manifest(content)
			test.mutate(&manifest)
			if _, _, err := fixture.engine.UploadExtension(context.Background(), fixture.actors[0], model.ExtensionUploadRequest{
				Manifest: manifest, Content: base64.StdEncoding.EncodeToString(content), IdempotencyKey: mustUUID(t),
			}); err == nil || !strings.Contains(err.Error(), "用途或 Schema") {
				t.Fatalf("domain mismatch err=%v", err)
			}
		})
	}
}

func TestWasmExtensionRuntimeRunsWithoutHostImports(t *testing.T) {
	policy := config.ExtensionPolicy{Sandbox: "wasm", MaxMemoryPages: 32, MaxOutputBytes: 1024}
	manifest := model.ExtensionManifest{Type: "wasm", Entrypoint: "_start"}
	output, exitCode, err := (wasmExtensionRuntime{}).Execute(context.Background(), policy, manifest, minimalWASMModule(), []byte(`{"ok":true}`))
	if err != nil || exitCode != 0 || output != "" {
		t.Fatalf("wasm output=%q exit=%d err=%v", output, exitCode, err)
	}
}

func TestWasmExtensionRuntimeRejectsUnknownHostImports(t *testing.T) {
	policy := config.ExtensionPolicy{Sandbox: "wasm", MaxMemoryPages: 32, MaxOutputBytes: 1024}
	manifest := model.ExtensionManifest{Type: "wasm", Entrypoint: "_start"}
	if _, _, err := (wasmExtensionRuntime{}).Execute(context.Background(), policy, manifest, wasmModuleWithUnknownImport(), nil); err == nil || !strings.Contains(err.Error(), "未授权") {
		t.Fatalf("unknown host import err=%v", err)
	}
}

func TestWasmExtensionRuntimeAllowsOnlyControlledStdinRead(t *testing.T) {
	policy := config.ExtensionPolicy{Sandbox: "wasm", MaxMemoryPages: 32, MaxOutputBytes: 1024}
	manifest := model.ExtensionManifest{Type: "wasm", Entrypoint: "_start"}
	if _, exitCode, err := (wasmExtensionRuntime{}).Execute(
		context.Background(), policy, manifest, wasmModuleWithWASIImport("fd_read"), []byte(`{"ok":true}`),
	); err != nil || exitCode != 0 {
		t.Fatalf("controlled stdin read exit=%d err=%v", exitCode, err)
	}
}

func TestWasmExtensionRuntimeRejectsFilesystemAndEnvironmentImports(t *testing.T) {
	policy := config.ExtensionPolicy{Sandbox: "wasm", MaxMemoryPages: 32, MaxOutputBytes: 1024}
	manifest := model.ExtensionManifest{Type: "wasm", Entrypoint: "_start"}
	for _, name := range []string{"path_open", "environ_get"} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := (wasmExtensionRuntime{}).Execute(
				context.Background(), policy, manifest, wasmModuleWithWASIImport(name), nil,
			); err == nil || !strings.Contains(err.Error(), "未授权") {
				t.Fatalf("import %s err=%v", name, err)
			}
		})
	}
}

func TestWasmExtensionRuntimeEnforcesMemoryAndDeadline(t *testing.T) {
	policy := config.ExtensionPolicy{Sandbox: "wasm", MaxMemoryPages: 32, MaxOutputBytes: 1024}
	manifest := model.ExtensionManifest{Type: "wasm", Entrypoint: "_start"}
	if _, _, err := (wasmExtensionRuntime{}).Execute(
		context.Background(), policy, manifest, wasmModuleWithMemoryPages(33), nil,
	); err == nil {
		t.Fatal("WASM module exceeded the configured memory limit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, exitCode, err := (wasmExtensionRuntime{}).Execute(
		ctx, policy, manifest, wasmModuleWithInfiniteLoop(), nil,
	); err == nil || exitCode != 124 || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("deadline exit=%d err=%v", exitCode, err)
	}
}

func TestExtensionInputAndOutputLimits(t *testing.T) {
	if _, err := canonicalExtensionInput([]byte(`{"ok":true}`), 4); err == nil {
		t.Fatal("oversized extension input was accepted")
	}
	if _, err := canonicalExtensionInput([]byte(`["not-an-object"]`), 1024); err == nil {
		t.Fatal("non-object extension input was accepted")
	}
	output := &boundedExtensionOutput{limit: 4}
	if count, err := output.Write([]byte("abcdef")); err == nil || count != 4 ||
		output.String() != "abcd" || !output.exceeded {
		t.Fatalf("bounded output count=%d value=%q exceeded=%v err=%v",
			count, output.String(), output.exceeded, err)
	}
}

func TestExtensionPlanHTTPAPIUsesPlanApprovalAndDoesNotReturnInput(t *testing.T) {
	fixture := newExtensionPlanFixture(t)
	handler := NewServer(fixture.engine, fixture.database)
	content := minimalWASMModule()
	manifest := fixture.manifest(content)
	uploaded := postExtensionJSON[model.ExtensionUploadResult](t, handler, fixture.actors[0], "/v1/extensions", model.ExtensionUploadRequest{
		Manifest: manifest, Content: base64.StdEncoding.EncodeToString(content), IdempotencyKey: mustUUID(t),
	}, http.StatusCreated)
	if !uploaded.Stored {
		t.Fatalf("uploaded=%+v", uploaded)
	}
	created := postExtensionJSON[model.ExtensionPlan](t, handler, fixture.actors[0], "/v1/extensions/plans", model.ExtensionPlanRequest{
		ExtensionID: manifest.ID, ExtensionVersion: manifest.Version, ObjectID: "service:demo",
		Input: []byte(`{"secret":"must-stay-in-runner"}`), IdempotencyKey: mustUUID(t),
	}, http.StatusCreated)
	if created.State != "pending_approval" || created.ConfirmationPhrase == "" ||
		created.ManifestDigest == "" || created.PolicyDigest == "" || created.MaxMemoryPages != 32 {
		t.Fatalf("created=%+v", created)
	}
	planBody := getExtensionBody(t, handler, fixture.actors[0], "/v1/extensions/plans/"+created.ID, http.StatusOK)
	if bytes.Contains(planBody, []byte("must-stay-in-runner")) {
		t.Fatal("extension plan API returned private input")
	}
	approved := postExtensionJSON[model.ExtensionPlan](t, handler, fixture.actors[1], "/v1/extensions/plans/"+created.ID+"/approve", model.ExtensionPlanApprovalRequest{
		Digest: created.PlanDigest, Confirmation: created.ConfirmationPhrase,
	}, http.StatusOK)
	if approved.State != "approved" || approved.ApprovedByHash != fixture.actors[1] || approved.ApprovalPolicy != model.ApprovalPolicyTwoParty {
		t.Fatalf("approval=%+v", approved)
	}
	executed := postExtensionJSON[model.ExtensionPlan](t, handler, fixture.actors[0], "/v1/extensions/plans/"+created.ID+"/execute", model.ExtensionPlanExecuteRequest{
		IdempotencyKey: mustUUID(t),
	}, http.StatusAccepted)
	if executed.State != "succeeded" || executed.Output != "extension-ok" {
		t.Fatalf("executed=%+v", executed)
	}
}

func TestExtensionExecutionRejectsApprovedPolicyDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*config.ExtensionPolicy)
	}{
		{name: "memory limit", mutate: func(policy *config.ExtensionPolicy) { policy.MaxMemoryPages++ }},
		{name: "publisher revoked", mutate: func(policy *config.ExtensionPolicy) { policy.TrustedPublishers = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExtensionPlanFixture(t)
			ctx := context.Background()
			content := minimalWASMModule()
			manifest := fixture.manifest(content)
			if _, _, err := fixture.engine.UploadExtension(ctx, fixture.actors[0], model.ExtensionUploadRequest{
				Manifest: manifest, Content: base64.StdEncoding.EncodeToString(content), IdempotencyKey: mustUUID(t),
			}); err != nil {
				t.Fatal(err)
			}
			plan, _, err := fixture.engine.CreateExtensionPlan(ctx, fixture.actors[0], model.ExtensionPlanRequest{
				ExtensionID: manifest.ID, ExtensionVersion: manifest.Version, ObjectID: "service:demo",
				Input: []byte(`{"operation":"inspect"}`), IdempotencyKey: mustUUID(t),
			})
			if err != nil {
				t.Fatal(err)
			}
			plan, err = fixture.engine.ApproveExtensionPlan(ctx, fixture.actors[1], plan.ID, model.ExtensionPlanApprovalRequest{
				Digest: plan.PlanDigest, Confirmation: plan.ConfirmationPhrase,
			})
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(fixture.engine.catalog.Extensions)
			finished, runErr := fixture.engine.ExecuteExtensionPlan(ctx, fixture.actors[0], plan.ID,
				model.ExtensionPlanExecuteRequest{IdempotencyKey: mustUUID(t)})
			if runErr == nil || finished.State != "failed" || fixture.runtime.calls != 0 {
				t.Fatalf("policy drift execution plan=%+v calls=%d err=%v", finished, fixture.runtime.calls, runErr)
			}
		})
	}
}

func TestExtensionPlanReadExpiresPendingPlan(t *testing.T) {
	fixture := newExtensionPlanFixture(t)
	ctx := context.Background()
	content := minimalWASMModule()
	manifest := fixture.manifest(content)
	if _, _, err := fixture.engine.UploadExtension(ctx, fixture.actors[0], model.ExtensionUploadRequest{
		Manifest: manifest, Content: base64.StdEncoding.EncodeToString(content), IdempotencyKey: mustUUID(t),
	}); err != nil {
		t.Fatal(err)
	}
	plan, _, err := fixture.engine.CreateExtensionPlan(ctx, fixture.actors[0], model.ExtensionPlanRequest{
		ExtensionID: manifest.ID, ExtensionVersion: manifest.Version, ObjectID: "service:demo",
		Input: []byte(`{"operation":"inspect"}`), IdempotencyKey: mustUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.ExpireExtensionPlans(ctx, plan.ExpiresAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	read, err := fixture.engine.ExtensionPlan(ctx, fixture.actors[0], plan.ID)
	if err != nil || read.State != "expired" {
		t.Fatalf("expired read=%+v err=%v", read, err)
	}
	if _, err := fixture.engine.ApproveExtensionPlan(ctx, fixture.actors[1], plan.ID, model.ExtensionPlanApprovalRequest{
		Digest: plan.PlanDigest, Confirmation: plan.ConfirmationPhrase,
	}); err == nil {
		t.Fatal("expired extension plan was approved")
	}
}

func TestExtensionPlanRecoveryMarksRunningNeedsAttention(t *testing.T) {
	fixture := newExtensionPlanFixture(t)
	ctx := context.Background()
	content := minimalWASMModule()
	manifest := fixture.manifest(content)
	if _, _, err := fixture.engine.UploadExtension(ctx, fixture.actors[0], model.ExtensionUploadRequest{
		Manifest: manifest, Content: base64.StdEncoding.EncodeToString(content), IdempotencyKey: mustUUID(t),
	}); err != nil {
		t.Fatal(err)
	}
	plan, _, err := fixture.engine.CreateExtensionPlan(ctx, fixture.actors[0], model.ExtensionPlanRequest{
		ExtensionID: manifest.ID, ExtensionVersion: manifest.Version, ObjectID: "service:demo",
		Input: []byte(`{"operation":"inspect"}`), IdempotencyKey: mustUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = fixture.engine.ApproveExtensionPlan(ctx, fixture.actors[1], plan.ID, model.ExtensionPlanApprovalRequest{
		Digest: plan.PlanDigest, Confirmation: plan.ConfirmationPhrase,
	})
	if err != nil {
		t.Fatalf("approval: %v", err)
	}
	if _, _, fresh, err := fixture.database.StartExtensionPlan(ctx, plan.ID, fixture.actors[0], mustUUID(t)); err != nil || !fresh {
		t.Fatalf("start running fresh=%v err=%v", fresh, err)
	}
	if _, err := NewEngineChecked(fixture.engine.catalog, fixture.database, &fakeExecutor{}, fixture.engine.stateRoot, WithExtensionRuntime(fixture.runtime)); err != nil {
		t.Fatal(err)
	}
	recovered, err := fixture.engine.ExtensionPlan(ctx, fixture.actors[0], plan.ID)
	if err != nil || recovered.State != "needs_attention" || !strings.Contains(recovered.Error, "Runner 重启") {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
}

type extensionPlanFixture struct {
	engine   *Engine
	database *store.Store
	runtime  *fakeExtensionRuntime
	actors   []string
	private  ed25519.PrivateKey
}

func newExtensionPlanFixture(t *testing.T) extensionPlanFixture {
	t.Helper()
	root := t.TempDir()
	database, err := store.Open(filepath.Join(root, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	actors := []string{
		config.AccessHashForEmail("extension-creator@example.test"),
		config.AccessHashForEmail("extension-approver-one@example.test"),
		config.AccessHashForEmail("extension-approver-two@example.test"),
		config.AccessHashForEmail("extension-executor@example.test"),
	}
	now := time.Now().UTC()
	principals := make(map[string]config.AccessPrincipal, len(actors))
	bindings := make([]model.RoleBinding, 0, len(actors))
	for index, actor := range actors {
		principals[actor] = config.AccessPrincipal{Subject: actor, TenantID: "tenant-a"}
		bindings = append(bindings, model.RoleBinding{ID: "extension-operator-" + string(rune('a'+index)), Subject: actor,
			TenantID: "tenant-a", RoleID: "extension-operator", ObjectIDs: []string{"*"}})
	}
	catalog := &config.Catalog{
		SchemaVersion: 4,
		Services: map[string]model.ServiceDefinition{
			"demo": {Name: "demo", ObjectID: "service:demo", DisplayName: "Demo", TenantID: "tenant-a"},
		},
		Access: &config.AccessPolicy{
			Enforced: true, DefaultTenant: "tenant-a",
			Tenants: map[string]model.Tenant{"tenant-a": {ID: "tenant-a", DisplayName: "Tenant A", Status: "active", CreatedAt: now}},
			Roles: map[string]model.Role{"extension-operator": {ID: "extension-operator", DisplayName: "Extension operator", Permissions: []model.Permission{
				model.PermissionRead, model.PermissionDeploy, model.PermissionManageConfig, model.PermissionInspect,
			}}},
			Principals: principals, Bindings: bindings,
		},
		Extensions: &config.ExtensionPolicy{
			Enabled: true, TrustedPublishers: []string{"release"},
			TrustedPublisherKeys: map[string]string{"release": base64.StdEncoding.EncodeToString(public)},
			RequireSignature:     true, Sandbox: "wasm", MaxPackageBytes: 1 << 20,
			MaxInputBytes: 4096, MaxOutputBytes: 4096, MaxExecutionSeconds: 5, MaxMemoryPages: 32,
		},
	}
	runtime := &fakeExtensionRuntime{}
	engine, err := NewEngineChecked(catalog, database, &fakeExecutor{}, root, WithExtensionRuntime(runtime))
	if err != nil {
		t.Fatal(err)
	}
	return extensionPlanFixture{engine: engine, database: database, runtime: runtime, actors: actors, private: private}
}

func (fixture extensionPlanFixture) manifest(content []byte) model.ExtensionManifest {
	manifest := model.ExtensionManifest{Purpose: model.ExtensionManifestPurpose, SchemaVersion: model.ExtensionManifestSchema,
		ID: "demo-wasm", Version: "v1", Type: "wasm", Entrypoint: "_start",
		Digest: digestText(string(content)), Publisher: "release", Permissions: []string{string(model.PermissionRead)}, AllowedObjects: []string{"service:demo"}}
	payload, _ := extensionSigningPayload(manifest)
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(fixture.private, payload))
	return manifest
}

func minimalWASMModule() []byte {
	return []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
		0x03, 0x02, 0x01, 0x00,
		0x07, 0x0a, 0x01, 0x06, 0x5f, 0x73, 0x74, 0x61, 0x72, 0x74, 0x00, 0x00,
		0x0a, 0x04, 0x01, 0x02, 0x00, 0x0b,
	}
}

func wasmModuleWithUnknownImport() []byte {
	return []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
		0x02, 0x0c, 0x01, 0x04, 0x65, 0x76, 0x69, 0x6c, 0x03, 0x72, 0x75, 0x6e, 0x00, 0x00,
	}
}

func wasmModuleWithWASIImport(name string) []byte {
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	types := []byte{2, 0x60, 0x04, 0x7f, 0x7f, 0x7f, 0x7f, 0x01, 0x7f, 0x60, 0x00, 0x00}
	module = appendWasmSection(module, 0x01, types)
	imports := appendWasmU32(nil, 1)
	imports = appendWasmString(imports, "wasi_snapshot_preview1")
	imports = appendWasmString(imports, name)
	imports = append(imports, 0x00, 0x00)
	module = appendWasmSection(module, 0x02, imports)
	module = appendWasmSection(module, 0x03, []byte{1, 1})
	exports := appendWasmU32(nil, 1)
	exports = appendWasmString(exports, "_start")
	exports = append(exports, 0x00, 0x01)
	module = appendWasmSection(module, 0x07, exports)
	module = appendWasmSection(module, 0x0a, []byte{1, 2, 0x00, 0x0b})
	return module
}

func wasmModuleWithMemoryPages(pages uint32) []byte {
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	module = appendWasmSection(module, 0x01, []byte{1, 0x60, 0, 0})
	module = appendWasmSection(module, 0x03, []byte{1, 0})
	memory := appendWasmU32(nil, 1)
	memory = append(memory, 0)
	memory = appendWasmU32(memory, pages)
	module = appendWasmSection(module, 0x05, memory)
	exports := appendWasmU32(nil, 1)
	exports = appendWasmString(exports, "_start")
	exports = append(exports, 0x00, 0x00)
	module = appendWasmSection(module, 0x07, exports)
	return appendWasmSection(module, 0x0a, []byte{1, 2, 0, 0x0b})
}

func wasmModuleWithInfiniteLoop() []byte {
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	module = appendWasmSection(module, 0x01, []byte{1, 0x60, 0, 0})
	module = appendWasmSection(module, 0x03, []byte{1, 0})
	exports := appendWasmU32(nil, 1)
	exports = appendWasmString(exports, "_start")
	exports = append(exports, 0x00, 0x00)
	module = appendWasmSection(module, 0x07, exports)
	return appendWasmSection(module, 0x0a, []byte{1, 7, 0, 0x03, 0x40, 0x0c, 0, 0x0b, 0x0b})
}

func appendWasmSection(module []byte, id byte, payload []byte) []byte {
	module = append(module, id)
	module = appendWasmU32(module, uint32(len(payload)))
	return append(module, payload...)
}

func appendWasmString(module []byte, value string) []byte {
	module = appendWasmU32(module, uint32(len(value)))
	return append(module, value...)
}

func appendWasmU32(module []byte, value uint32) []byte {
	for value >= 0x80 {
		module = append(module, byte(value)|0x80)
		value >>= 7
	}
	return append(module, byte(value))
}

func postExtensionJSON[T any](
	t *testing.T,
	handler http.Handler,
	actor, path string,
	body any,
	wantStatus int,
) T {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	request.Header.Set(actorHeader, actor)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("POST %s status=%d want=%d body=%s", path, response.Code, wantStatus, response.Body.String())
	}
	var result T
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode POST %s response: %v body=%s", path, err, response.Body.String())
	}
	return result
}

func getExtensionBody(t *testing.T, handler http.Handler, actor, path string, wantStatus int) []byte {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set(actorHeader, actor)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("GET %s status=%d want=%d body=%s", path, response.Code, wantStatus, response.Body.String())
	}
	return response.Body.Bytes()
}
