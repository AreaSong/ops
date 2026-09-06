package runner

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

type fleetRunnerUpdateFixture struct {
	engine  *Engine
	db      *store.Store
	request model.FleetRunnerUpdatePlanRequest
	actors  []string
	nonce   int
}

func newFleetRunnerUpdateFixture(t *testing.T) *fleetRunnerUpdateFixture {
	t.Helper()
	root := t.TempDir()
	database, err := store.Open(filepath.Join(root, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifactRoot := filepath.Join(root, "incoming")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	artifactContent := "signed-fleet-runner-v2"
	if err := os.WriteFile(filepath.Join(artifactRoot, "runner-v2"), []byte(artifactContent), 0o700); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(root, "runner-current")
	if err := os.WriteFile(binaryPath, []byte("runner-v1"), 0o700); err != nil {
		t.Fatal(err)
	}
	actors, access := fleetRunnerUpdateAccessPolicy()
	catalog := fleetRunnerUpdateCatalog(
		access, publicKey, artifactRoot, binaryPath,
	)
	engine, err := NewEngineChecked(catalog, database, &fakeExecutor{}, root)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &fleetRunnerUpdateFixture{engine: engine, db: database, actors: actors}
	t.Cleanup(func() {
		engine.Stop()
		engine.Wait()
		_ = database.Close()
	})
	for _, runnerID := range []string{"runner-a", "runner-b"} {
		fixture.heartbeat(t, runnerID, "v1", strings.Repeat("a", 40), "sha256:"+strings.Repeat("a", 64))
	}
	now := time.Now().UTC()
	manifest := model.FleetRunnerUpdateManifest{
		Purpose: model.FleetRunnerUpdateManifestPurpose, Schema: model.FleetRunnerUpdateManifestSchema,
		GOOS: "linux", GOARCH: "amd64", TargetVersion: "v2",
		ArtifactDigest: digestText(artifactContent), ArtifactRevision: strings.Repeat("b", 40), Publisher: "release",
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	fixture.request = model.FleetRunnerUpdatePlanRequest{
		Manifest: manifest, ArtifactPath: "runner-v2",
		ArtifactSignature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)),
		TargetRunnerIDs:   []string{"runner-a", "runner-b"},
		BatchPolicy: model.BatchPolicy{
			Strategy: model.BatchCanary, CanarySize: 1, BatchSize: 1, ObservationSeconds: 30,
		},
		MaxConcurrent: 1, RollbackOnFailure: true,
		ChangeWindow:   model.ChangeWindow{StartAt: now.Add(-time.Minute), EndAt: now.Add(time.Hour), Timezone: "UTC"},
		Confirmation:   "创建 Runner Fleet 更新到 v2，目标 2 个",
		IdempotencyKey: "11111111-1111-4111-8111-111111111111",
	}
	return fixture
}

func fleetRunnerUpdateAccessPolicy() ([]string, *config.AccessPolicy) {
	actors := []string{strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64)}
	policy := &config.AccessPolicy{
		Enforced: true, DefaultTenant: "tenant-a",
		Tenants: map[string]model.Tenant{
			"tenant-a": {ID: "tenant-a", DisplayName: "Tenant A", Status: "active", CreatedAt: time.Now().UTC()},
		},
		Roles: map[string]model.Role{
			"runner-updater": {
				ID: "runner-updater", DisplayName: "Runner updater",
				Permissions: []model.Permission{model.PermissionRead, model.PermissionRunnerUpdate},
			},
		},
		Principals: make(map[string]config.AccessPrincipal),
	}
	for index, actor := range actors {
		policy.Principals[actor] = config.AccessPrincipal{Subject: actor, TenantID: "tenant-a"}
		policy.Bindings = append(policy.Bindings, model.RoleBinding{
			ID: "runner-update-actor-" + string(rune('a'+index)), Subject: actor,
			TenantID: "tenant-a", RoleID: "runner-updater",
			ObjectIDs: []string{"runner:runner-a", "runner:runner-b"},
		})
	}
	return actors, policy
}

func fleetRunnerUpdateCatalog(
	access *config.AccessPolicy,
	heartbeatKey ed25519.PublicKey,
	artifactRoot, binaryPath string,
) *config.Catalog {
	key := base64.StdEncoding.EncodeToString(heartbeatKey)
	fingerprintA := "sha256:" + strings.Repeat("c", 64)
	fingerprintB := "sha256:" + strings.Repeat("d", 64)
	servers := []model.ServerNode{
		{ID: "server-a", Hostname: "server-a", Environment: "test", RunnerID: "runner-a", State: model.NodeUnknown},
		{ID: "server-b", Hostname: "server-b", Environment: "test", RunnerID: "runner-b", State: model.NodeUnknown},
	}
	runners := []model.RunnerNode{
		{ID: "runner-a", ServerID: "server-a", TenantID: "tenant-a", Hostname: "runner-a", Version: "v1", State: model.NodeUnknown, Capabilities: []string{"runner-update"}, CertificateFingerprint: fingerprintA, HeartbeatPublicKey: key},
		{ID: "runner-b", ServerID: "server-b", TenantID: "tenant-a", Hostname: "runner-b", Version: "v1", State: model.NodeUnknown, Capabilities: []string{"runner-update"}, CertificateFingerprint: fingerprintB, HeartbeatPublicKey: key},
	}
	return &config.Catalog{
		SchemaVersion: 4, Services: map[string]model.ServiceDefinition{}, Access: access,
		Fleet: &config.FleetPolicy{
			Enabled: true, HeartbeatTimeoutSeconds: 300, AllowRemoteRunners: true,
			RequiremTLS: true, RequireSignedHeartbeat: true,
			RunnerPublicKeys: map[string]string{"runner-a": key, "runner-b": key},
			Inventory:        model.Fleet{Servers: servers, Runners: runners},
		},
		RunnerUpdate: &config.RunnerUpdatePolicy{
			Enabled: true, FleetEnabled: true, RunnerID: "runner-a", ArtifactRoot: artifactRoot,
			BinaryPath: binaryPath, UnitName: "areasong-ops-runner.service",
			UpdaterUnitName: "areasong-ops-runner-update@.service", Publisher: "release",
			ManifestGOOS: "linux", ManifestGOARCH: "amd64",
			TrustedPublisherKeys: map[string]string{"release": key}, MaxArtifactBytes: 1 << 20,
		},
	}
}

func (fixture *fleetRunnerUpdateFixture) heartbeat(
	t *testing.T,
	runnerID, version, revision, binaryDigest string,
) model.RunnerNode {
	t.Helper()
	fixture.nonce++
	input := RunnerHeartbeatRequest{
		PayloadVersion: RunnerIdentityPayloadVersion, RunnerID: runnerID,
		Version: version, Revision: revision, BinaryDigest: binaryDigest,
		Capabilities: []string{"runner-update"}, Labels: map[string]string{"role": "test"},
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Nonce:     runnerID + "-nonce-" + string(rune('a'+fixture.nonce)),
	}
	payloadDigest, err := HeartbeatPayloadDigest(input)
	if err != nil {
		t.Fatal(err)
	}
	node, err := fixture.engine.HeartbeatRunnerAuthenticated(context.Background(), runnerID, input, payloadDigest)
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func (fixture *fleetRunnerUpdateFixture) createPlan(t *testing.T) model.FleetRunnerUpdatePlan {
	t.Helper()
	plan, created, err := fixture.engine.CreateFleetRunnerUpdatePlan(context.Background(), fixture.actors[0], fixture.request)
	if err != nil || !created {
		t.Fatalf("create plan created=%v err=%v", created, err)
	}
	return plan
}

func TestFleetRunnerUpdateRequestRejectsImplicitOrUnsafeTargets(t *testing.T) {
	now := time.Now().UTC()
	valid := model.FleetRunnerUpdatePlanRequest{
		Manifest:     model.FleetRunnerUpdateManifest{TargetVersion: "v2"},
		ArtifactPath: "runner-v2", TargetRunnerIDs: []string{"runner-a"},
		BatchPolicy: model.BatchPolicy{Strategy: model.BatchSerial}, MaxConcurrent: 1,
		RollbackOnFailure: true,
		ChangeWindow:      model.ChangeWindow{StartAt: now.Add(-time.Minute), EndAt: now.Add(time.Hour), Timezone: "UTC"},
		Confirmation:      "创建 Runner Fleet 更新到 v2，目标 1 个",
		IdempotencyKey:    "11111111-1111-4111-8111-111111111111",
	}
	for name, mutate := range map[string]func(*model.FleetRunnerUpdatePlanRequest){
		"empty":    func(value *model.FleetRunnerUpdatePlanRequest) { value.TargetRunnerIDs = nil },
		"wildcard": func(value *model.FleetRunnerUpdatePlanRequest) { value.TargetRunnerIDs = []string{"runner-*"} },
		"duplicate": func(value *model.FleetRunnerUpdatePlanRequest) {
			value.TargetRunnerIDs = []string{"runner-a", "runner-a"}
		},
		"no canary": func(value *model.FleetRunnerUpdatePlanRequest) {
			value.TargetRunnerIDs = []string{"runner-a", "runner-b"}
			value.Confirmation = "创建 Runner Fleet 更新到 v2，目标 2 个"
		},
		"unbounded concurrency": func(value *model.FleetRunnerUpdatePlanRequest) { value.MaxConcurrent = 2 },
		"rollback disabled":     func(value *model.FleetRunnerUpdatePlanRequest) { value.RollbackOnFailure = false },
	} {
		t.Run(name, func(t *testing.T) {
			request := valid
			mutate(&request)
			if err := validateFleetRunnerUpdateRequest(request); err == nil {
				t.Fatal("unsafe Fleet update request was accepted")
			}
		})
	}
}

func TestFleetRunnerUpdateRequiresIndependentApprovalAndCreatorExecution(t *testing.T) {
	fixture := newFleetRunnerUpdateFixture(t)
	plan := fixture.createPlan(t)
	if _, err := fixture.engine.ApproveFleetRunnerUpdatePlan(context.Background(), fixture.actors[0], plan.ID, model.FleetRunnerUpdateApprovalRequest{Digest: plan.PlanDigest, Confirmation: plan.ConfirmationPhrase}); err == nil {
		t.Fatal("creator approved its own Fleet update")
	}
	first, err := fixture.engine.ApproveFleetRunnerUpdatePlan(context.Background(), fixture.actors[1], plan.ID, model.FleetRunnerUpdateApprovalRequest{Digest: plan.PlanDigest, Confirmation: plan.ConfirmationPhrase})
	if err != nil || first.State != model.FleetRunnerUpdateApproved || first.ApprovedByHash != fixture.actors[1] || first.SecondApprovedByHash != "" {
		t.Fatalf("approval state=%s err=%v", first.State, err)
	}
	for _, actor := range fixture.actors[1:3] {
		if _, _, err := fixture.engine.ExecuteFleetRunnerUpdatePlan(context.Background(), actor, plan.ID, model.FleetRunnerUpdateExecuteRequest{IdempotencyKey: "22222222-2222-4222-8222-222222222222"}); err == nil {
			t.Fatalf("non-independent actor %s executed Fleet update", actor[:4])
		}
	}
	started, fresh, err := fixture.engine.ExecuteFleetRunnerUpdatePlan(context.Background(), fixture.actors[0], plan.ID, model.FleetRunnerUpdateExecuteRequest{IdempotencyKey: "22222222-2222-4222-8222-222222222222"})
	if err != nil || !fresh || started.State != model.FleetRunnerUpdateRunning {
		t.Fatalf("execute fresh=%v state=%s err=%v", fresh, started.State, err)
	}
	if _, fresh, err := fixture.engine.ExecuteFleetRunnerUpdatePlan(context.Background(), fixture.actors[0], plan.ID, model.FleetRunnerUpdateExecuteRequest{IdempotencyKey: "22222222-2222-4222-8222-222222222222"}); err != nil || fresh {
		t.Fatalf("execution replay fresh=%v err=%v", fresh, err)
	}

	tamperedFixture := newFleetRunnerUpdateFixture(t)
	tamperedPlan := tamperedFixture.createPlan(t)
	if err := os.WriteFile(tamperedPlan.StagedPath, []byte("tampered"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := tamperedFixture.engine.ApproveFleetRunnerUpdatePlan(context.Background(), tamperedFixture.actors[1], tamperedPlan.ID, model.FleetRunnerUpdateApprovalRequest{Digest: tamperedPlan.PlanDigest, Confirmation: tamperedPlan.ConfirmationPhrase}); err == nil || !strings.Contains(err.Error(), "摘要") {
		t.Fatalf("tampered staged artifact approval err=%v", err)
	}
}

func TestFleetRunnerUpdateCanaryFailureStopsAndRollsBack(t *testing.T) {
	fixture := newFleetRunnerUpdateFixture(t)
	plan := fixture.createPlan(t)
	ctx := context.Background()
	if _, err := fixture.db.ApproveFleetRunnerUpdatePlan(ctx, plan.ID, fixture.actors[1], plan.PlanDigest, plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.db.StartFleetRunnerUpdatePlan(ctx, plan.ID, fixture.actors[0], "execution-key"); err != nil {
		t.Fatal(err)
	}
	canary := claimFleetRunnerUpdateForTest(t, fixture.db, "runner-a")
	completeFleetRunnerUpdateForTest(t, fixture.db, canary, "succeeded", "v2", strings.Repeat("b", 40), plan.Manifest.ArtifactDigest, "")
	fixture.heartbeat(t, "runner-a", "v2", strings.Repeat("b", 40), plan.Manifest.ArtifactDigest)
	now := time.Now().UTC()
	if done, err := fixture.engine.reconcileFleetRunnerUpdate(ctx, plan.ID, now); err != nil || done {
		t.Fatalf("begin observation done=%v err=%v", done, err)
	}
	observing, err := fixture.db.GetFleetRunnerUpdatePlan(ctx, plan.ID)
	if err != nil || observing.State != model.FleetRunnerUpdateObserving || observing.ObservationEndsAt == nil {
		t.Fatalf("observing plan=%+v err=%v", observing, err)
	}
	if _, claimed, err := fixture.db.ClaimFleetRunnerUpdate(ctx, "runner-b", time.Minute); err != nil || claimed {
		t.Fatalf("next batch escaped observation claimed=%v err=%v", claimed, err)
	}
	if _, err := fixture.engine.reconcileFleetRunnerUpdate(ctx, plan.ID, observing.ObservationEndsAt.Add(-time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.engine.reconcileFleetRunnerUpdate(ctx, plan.ID, *observing.ObservationEndsAt); err != nil {
		t.Fatal(err)
	}
	second := claimFleetRunnerUpdateForTest(t, fixture.db, "runner-b")
	completeFleetRunnerUpdateForTest(t, fixture.db, second, "failed", "v1", strings.Repeat("a", 40), "sha256:"+strings.Repeat("a", 64), "updater failed")
	fixture.heartbeat(t, "runner-b", "v1", strings.Repeat("a", 40), "sha256:"+strings.Repeat("a", 64))
	if _, err := fixture.engine.reconcileFleetRunnerUpdate(ctx, plan.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	rollingBack, err := fixture.db.GetFleetRunnerUpdatePlan(ctx, plan.ID)
	if err != nil || rollingBack.State != model.FleetRunnerUpdateRollingBack {
		t.Fatalf("rollback state=%s err=%v", rollingBack.State, err)
	}
	rollback := claimFleetRunnerUpdateForTest(t, fixture.db, "runner-a")
	if rollback.Action != "rollback" {
		t.Fatalf("rollback action=%s", rollback.Action)
	}
	completeFleetRunnerUpdateForTest(t, fixture.db, rollback, "rolled_back", "v1", strings.Repeat("a", 40), "sha256:"+strings.Repeat("a", 64), "")
	fixture.heartbeat(t, "runner-a", "v1", strings.Repeat("a", 40), "sha256:"+strings.Repeat("a", 64))
	if done, err := fixture.engine.reconcileFleetRunnerUpdate(ctx, plan.ID, time.Now().UTC()); err != nil || !done {
		t.Fatalf("finish rollback done=%v err=%v", done, err)
	}
	finished, err := fixture.db.GetFleetRunnerUpdatePlan(ctx, plan.ID)
	if err != nil || finished.State != model.FleetRunnerUpdateRolledBack {
		t.Fatalf("finished state=%s err=%v", finished.State, err)
	}
}

func claimFleetRunnerUpdateForTest(
	t *testing.T,
	database *store.Store,
	runnerID string,
) model.FleetRunnerUpdateAssignment {
	t.Helper()
	assignment, claimed, err := database.ClaimFleetRunnerUpdate(context.Background(), runnerID, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim %s claimed=%v err=%v", runnerID, claimed, err)
	}
	return assignment
}

func completeFleetRunnerUpdateForTest(
	t *testing.T,
	database *store.Store,
	assignment model.FleetRunnerUpdateAssignment,
	state, version, revision, digest, errorText string,
) {
	t.Helper()
	_, fresh, err := database.CompleteFleetRunnerUpdate(context.Background(), assignment.RunnerID, assignment.ItemID, model.FleetRunnerUpdateCompletionRequest{
		FleetRunnerUpdateFence: assignment.Fence, IdempotencyKey: fleetRunnerCompletionKey(assignment),
		State: state, ObservedVersion: version, ObservedRevision: revision, ObservedDigest: digest, Error: errorText,
	})
	if err != nil || !fresh {
		t.Fatalf("complete %s fresh=%v err=%v", assignment.RunnerID, fresh, err)
	}
}

func TestFleetRunnerUpdateCrossTenantAndOfflineTargetsFailClosed(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, *fleetRunnerUpdateFixture){
		"cross tenant": func(t *testing.T, fixture *fleetRunnerUpdateFixture) {
			node, found, err := fixture.db.GetRunnerNode(context.Background(), "runner-b")
			if err != nil || !found {
				t.Fatal(err)
			}
			node.TenantID = "tenant-b"
			if err := fixture.db.UpsertRunnerNode(context.Background(), node, "tenant-b"); err != nil {
				t.Fatal(err)
			}
		},
		"offline": func(t *testing.T, fixture *fleetRunnerUpdateFixture) {
			node, found, err := fixture.db.GetRunnerNode(context.Background(), "runner-b")
			if err != nil || !found {
				t.Fatal(err)
			}
			node.State, node.LeaseExpiresAt = model.NodeOffline, nil
			if err := fixture.db.UpsertRunnerNode(context.Background(), node, node.TenantID); err != nil {
				t.Fatal(err)
			}
		},
		"missing capability": func(t *testing.T, fixture *fleetRunnerUpdateFixture) {
			node, found, err := fixture.db.GetRunnerNode(context.Background(), "runner-b")
			if err != nil || !found {
				t.Fatal(err)
			}
			node.Capabilities = []string{"backup"}
			if err := fixture.db.UpsertRunnerNode(context.Background(), node, node.TenantID); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newFleetRunnerUpdateFixture(t)
			mutate(t, fixture)
			if _, _, err := fixture.engine.CreateFleetRunnerUpdatePlan(context.Background(), fixture.actors[0], fixture.request); err == nil {
				t.Fatal("unsafe target was accepted")
			}
		})
	}
}

func TestFleetRunnerUpdatePolicyDigestChangeInvalidatesApproval(t *testing.T) {
	fixture := newFleetRunnerUpdateFixture(t)
	plan := fixture.createPlan(t)
	fixture.engine.catalog.RunnerUpdate.MaxArtifactBytes++
	_, err := fixture.engine.ApproveFleetRunnerUpdatePlan(context.Background(), fixture.actors[1], plan.ID, model.FleetRunnerUpdateApprovalRequest{
		Digest: plan.PlanDigest, Confirmation: plan.ConfirmationPhrase,
	})
	if err == nil || !strings.Contains(err.Error(), "策略摘要") {
		t.Fatalf("policy drift approval err=%v", err)
	}
}
