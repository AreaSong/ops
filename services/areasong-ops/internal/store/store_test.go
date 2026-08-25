package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestOpenMigratesExistingPlanSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(migrations[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var version int
	if err := migrated.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) {
		t.Fatalf("schema version=%d want=%d", version, len(migrations))
	}
	var column string
	if err := migrated.db.QueryRow(`
		SELECT name FROM pragma_table_info('release_plans') WHERE name = 'maintenance_silence_id'
	`).Scan(&column); err != nil || column != "maintenance_silence_id" {
		t.Fatalf("column=%q err=%v", column, err)
	}
	for _, expected := range []string{"expected_before_digest", "required_roles_json"} {
		if err := migrated.db.QueryRow(`
			SELECT name FROM pragma_table_info('recovery_points') WHERE name = ?
		`, expected).Scan(&column); err != nil || column != expected {
			t.Fatalf("column=%q expected=%q err=%v", column, expected, err)
		}
	}
	if err := migrated.db.QueryRow(`
		SELECT name FROM pragma_table_info('kubernetes_plans') WHERE name = 'execute_idempotency_key'
	`).Scan(&column); err != nil || column != "execute_idempotency_key" {
		t.Fatalf("column=%q err=%v", column, err)
	}
}

func TestCredentialRotationStoresOnlySummaryAndClosesIdempotently(t *testing.T) {
	ctx := context.Background()
	database, err := Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	rotation := model.CredentialRotation{
		ID:             "11111111-1111-4111-8111-111111111111",
		IdempotencyKey: "22222222-2222-4222-8222-222222222222",
		ActorHash:      strings.Repeat("a", 64), CredentialType: model.GitHubAlertmanagerCredential,
		Target: "fixed target", State: model.CredentialRotationRunning,
		Fingerprint: "sha256:0123456789ab", ExpiresAt: "2027-08-12", CreatedAt: time.Now().UTC(),
	}
	created, fresh, err := database.StartCredentialRotation(ctx, rotation)
	if err != nil || !fresh || created.ID != rotation.ID {
		t.Fatalf("rotation=%+v fresh=%v err=%v", created, fresh, err)
	}
	if err := database.FinishCredentialRotation(ctx, rotation.ID, model.CredentialRotationResult{
		State:            model.CredentialRotationSwitchedPendingRevocation,
		ValidationResult: "passed", Outcome: "switched", RollbackResult: "retained",
	}); err != nil {
		t.Fatal(err)
	}
	verified, err := database.MarkCredentialRevocationVerified(ctx, rotation.ID, rotation.ActorHash,
		"33333333-3333-4333-8333-333333333333")
	if err != nil || verified.State != model.CredentialRotationRevocationVerified {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}
	closed, fresh, err := database.CloseCredentialRotation(ctx, rotation.ID, rotation.ActorHash,
		"33333333-3333-4333-8333-333333333333", "old token revoked")
	if err != nil || !fresh || closed.State != model.CredentialRotationCompleted {
		t.Fatalf("closed=%+v fresh=%v err=%v", closed, fresh, err)
	}
	closed, fresh, err = database.CloseCredentialRotation(ctx, rotation.ID, rotation.ActorHash,
		"33333333-3333-4333-8333-333333333333", "old token revoked")
	if err != nil || fresh || closed.State != model.CredentialRotationCompleted {
		t.Fatalf("idempotent close=%+v fresh=%v err=%v", closed, fresh, err)
	}
	data, err := os.ReadFile(database.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "github_pat_") || strings.Contains(string(data), "GITHUB_TOKEN=") {
		t.Fatal("SQLite unexpectedly contains credential material")
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestRecoveryPointExpiryControlsOperationProtection(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	now := time.Now().UTC()
	database.now = func() time.Time { return now }
	preview := testPreview(now)
	if err := database.CreatePreview(ctx, PreviewInput{
		Preview: preview, ConfirmationHash: HashConfirmation("重启 demo"),
	}); err != nil {
		t.Fatal(err)
	}
	task, _, err := database.StartTask(ctx, "a", model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "重启 demo", IdempotencyKey: "recovery-protection",
	}, "task-recovery-protection")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunning(ctx, task.ID, "backup"); err != nil {
		t.Fatal(err)
	}
	verifiedAt := now
	recoverableUntil := now.Add(time.Hour)
	point := model.RecoveryPoint{
		ID: "point-protection", TaskID: task.ID, Service: task.Service, Status: "verified",
		Evidence:       model.RecoveryPointEvidence{SchemaVersion: 1, Service: task.Service, TaskID: task.ID, CreatedAt: now},
		EvidenceDigest: "sha256:evidence", ExpectedBeforeDigest: "sha256:before",
		RequiredArtifactRoles: []string{"postgres-demo"}, CreatedAt: now,
		VerifiedAt: &verifiedAt, RecoverableUntil: &recoverableUntil,
	}
	if err := database.SaveRecoveryPoint(ctx, point); err != nil {
		t.Fatal(err)
	}
	protected, err := database.ProtectedOperationIDs(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := protected[task.ID]; !exists {
		t.Fatal("verified recovery point did not protect operation")
	}
	count, err := database.ExpireRecoveryPoints(ctx, now.Add(2*time.Hour))
	if err != nil || count != 1 {
		t.Fatalf("expired=%d err=%v", count, err)
	}
	protected, err = database.ProtectedOperationIDs(ctx, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := protected[task.ID]; exists {
		t.Fatal("expired recovery point still protects operation")
	}
	if err := database.MarkProductionChanged(ctx, task.ID, true, "rollback source"); err != nil {
		t.Fatal(err)
	}
	protected, err = database.ProtectedOperationIDs(ctx, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := protected[task.ID]; !exists {
		t.Fatal("rollback source did not protect operation")
	}
}

func testPreview(now time.Time) model.Preview {
	return model.Preview{
		ID: "preview-test", ActorHash: "a", Service: "demo", Action: "restart",
		Risk: model.RiskMedium, Impact: "短暂中断", Rollback: "重新启动", Scope: "单服务",
		Steps:    []string{"preflight", "restart", "health"},
		Snapshot: map[string]any{"version": "1.0.0"}, CreatedAt: now,
		ExpiresAt: now.Add(5 * time.Minute),
	}
}

func TestLatestSucceededRestoreDrillRequiresExactArtifactEvidence(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	now := time.Now().UTC()
	insert := func(id, digest string, state model.TaskState, finished time.Time) {
		t.Helper()
		_, err := database.db.ExecContext(ctx, `
			INSERT INTO tasks (
				id, idempotency_key, request_hash, actor_hash, service, action, target, risk,
				state, preview_id, snapshot_json, stages_json, restore_mode,
				restore_evidence_digest, created_at, finished_at
			) VALUES (?, ?, ?, ?, 'demo', 'restore-drill', 'point', ?, ?, ?, '{}', '[]',
			          'isolated', ?, ?, ?)
		`, id, "idem-"+id, "request-"+id, "actor", model.RiskMedium, state,
			"preview-"+id, digest, timeText(finished.Add(-time.Minute)), timeText(finished))
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("drill-old-artifact", "sha256:old", model.TaskSucceeded, now.Add(-time.Hour))
	insert("drill-failed-current", "sha256:current", model.TaskNeedsAttention, now.Add(-time.Minute))

	if task, found, err := database.LatestSucceededRestoreDrill(ctx, "demo", "sha256:current"); err != nil || found {
		t.Fatalf("current artifact unexpectedly fresh: task=%+v found=%v err=%v", task, found, err)
	}
	insert("drill-current", "sha256:current", model.TaskSucceeded, now)
	task, found, err := database.LatestSucceededRestoreDrill(ctx, "demo", "sha256:current")
	if err != nil || !found || task.ID != "drill-current" || task.FinishedAt == nil {
		t.Fatalf("task=%+v found=%v err=%v", task, found, err)
	}
	if _, found, err := database.LatestSucceededRestoreDrill(ctx, "demo", "sha256:other"); err != nil || found {
		t.Fatalf("different artifact found=%v err=%v", found, err)
	}
}

func TestStartTaskIsIdempotentAndConsumesPreview(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Now().UTC()
	store.now = func() time.Time { return now }
	preview := testPreview(now)
	if err := store.CreatePreview(ctx, PreviewInput{
		Preview: preview, ConfirmationHash: HashConfirmation("重启 demo"),
	}); err != nil {
		t.Fatal(err)
	}
	request := model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "重启 demo", IdempotencyKey: "idem-1",
	}
	first, created, err := store.StartTask(ctx, "a", request, "task-1")
	if err != nil || !created {
		t.Fatalf("first start: created=%v err=%v", created, err)
	}
	second, created, err := store.StartTask(ctx, "a", request, "task-2")
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("idempotent start: task=%+v created=%v err=%v", second, created, err)
	}
	request.Confirmation = "其他请求"
	if _, _, err := store.StartTask(ctx, "a", request, "task-3"); err != ErrIdempotency {
		t.Fatalf("expected ErrIdempotency, got %v", err)
	}
	otherPreview := testPreview(now)
	otherPreview.ID = "preview-other"
	if err := store.CreatePreview(ctx, PreviewInput{
		Preview: otherPreview, ConfirmationHash: HashConfirmation("重启 demo"),
	}); err != nil {
		t.Fatal(err)
	}
	request.PreviewID = otherPreview.ID
	request.Confirmation = "重启 demo"
	if _, _, err := store.StartTask(ctx, "a", request, "task-4"); err != ErrIdempotency {
		t.Fatalf("expected preview-bound ErrIdempotency, got %v", err)
	}
}

func TestStartTaskRejectsWrongConfirmation(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Now().UTC()
	store.now = func() time.Time { return now }
	preview := testPreview(now)
	if err := store.CreatePreview(ctx, PreviewInput{
		Preview: preview, ConfirmationHash: HashConfirmation("重启 demo"),
	}); err != nil {
		t.Fatal(err)
	}
	_, _, err := store.StartTask(ctx, "a", model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "restart demo", IdempotencyKey: "idem-2",
	}, "task-2")
	if err != ErrConfirmation {
		t.Fatalf("expected ErrConfirmation, got %v", err)
	}
}

func TestBatchCoordinatorLeaseTakeoverFencesOldOwner(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	now := time.Now().UTC()
	database.now = func() time.Time { return now }
	operation := testBatchOperation(now, "batch-lease")
	if _, created, err := database.CreateBatchOperation(ctx, BatchOperationInput{
		Operation: operation, ConfirmationHash: HashConfirmation(operation.ConfirmationPhrase),
	}); err != nil || !created {
		t.Fatalf("create batch: created=%v err=%v", created, err)
	}
	tokenA, generationA, acquired, err := database.AcquireBatchCoordinator(ctx, operation.ID, "owner-a", time.Minute)
	if err != nil || !acquired || generationA != 1 || tokenA == "" {
		t.Fatalf("owner-a token=%q generation=%d acquired=%v err=%v", tokenA, generationA, acquired, err)
	}
	if token, generation, acquired, err := database.AcquireBatchCoordinator(ctx, operation.ID, "owner-b", time.Minute); err != nil || acquired || token != "" || generation != generationA {
		t.Fatalf("owner-b active lease token=%q generation=%d acquired=%v err=%v", token, generation, acquired, err)
	}
	now = now.Add(2 * time.Minute)
	tokenB, generationB, acquired, err := database.AcquireBatchCoordinator(ctx, operation.ID, "owner-b", time.Minute)
	if err != nil || !acquired || generationB != 2 || tokenB == "" || tokenB == tokenA {
		t.Fatalf("takeover token=%q generation=%d acquired=%v err=%v", tokenB, generationB, acquired, err)
	}
	if renewed, err := database.RenewBatchCoordinator(ctx, operation.ID, "owner-a", tokenA, time.Minute); err != nil || renewed {
		t.Fatalf("old lease renewed=%v err=%v", renewed, err)
	}
	item := operation.Items[0]
	err = database.UpdateBatchItemCAS(ctx, operation.ID, item.ID, model.BatchNodePending, model.BatchNodeReady, "", "", "",
		BatchCoordinatorFence{Owner: "owner-a", Token: tokenA})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("old fence update error=%v, want ErrNotFound", err)
	}
	if err := database.UpdateBatchItemCAS(ctx, operation.ID, item.ID, model.BatchNodePending, model.BatchNodeReady, "", "", "",
		BatchCoordinatorFence{Owner: "owner-b", Token: tokenB}); err != nil {
		t.Fatal(err)
	}
}

func TestBatchCoordinatorMissingJobDoesNotCreateLease(t *testing.T) {
	database := openTestStore(t)
	if token, generation, acquired, err := database.AcquireBatchCoordinator(context.Background(), "missing", "owner", time.Minute); err != nil || acquired || token != "" || generation != 0 {
		t.Fatalf("token=%q generation=%d acquired=%v err=%v", token, generation, acquired, err)
	}
}

func testBatchOperation(now time.Time, id string) model.BatchOperation {
	item := model.BatchItem{ID: "item-1", ObjectID: "service:demo", Service: "demo", BatchIndex: 0, State: model.BatchNodePending, UpdatedAt: now}
	return model.BatchOperation{
		ID: id, IdempotencyKey: id + "-idempotency", ActorHash: strings.Repeat("a", 64), TenantID: "default",
		Action: "restart", Digest: "digest", ConfirmationPhrase: "批量重启 1 项", State: model.BatchRunning,
		Task: model.BatchTask{ID: id, Action: "restart", TargetIDs: []string{"demo"}, Nodes: []model.DAGNode{{ID: item.ID, State: model.BatchNodePending}},
			BatchPolicy: model.BatchPolicy{Strategy: model.BatchFixed, BatchSize: 1}, Concurrency: model.ConcurrencyPolicy{Scope: model.ConcurrencyGlobal, MaxConcurrent: 1},
			FailurePolicy: model.FailureStop, State: model.BatchTaskRunning, CreatedAt: now},
		Items: []model.BatchItem{item}, CreatedAt: now, UpdatedAt: now,
	}
}

func TestRecoverInterruptedFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Now().UTC()
	store.now = func() time.Time { return now }
	preview := testPreview(now)
	if err := store.CreatePreview(ctx, PreviewInput{
		Preview: preview, ConfirmationHash: HashConfirmation("重启 demo"),
	}); err != nil {
		t.Fatal(err)
	}
	request := model.StartTaskRequest{PreviewID: preview.ID, Confirmation: "重启 demo", IdempotencyKey: "idem-3"}
	if _, _, err := store.StartTask(ctx, "a", request, "task-3"); err != nil {
		t.Fatal(err)
	}
	count, err := store.RecoverInterrupted(ctx, func(string, string, string, bool) (bool, bool) {
		return true, false
	})
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	task, err := store.GetTask(ctx, "task-3")
	if err != nil || task.State != model.TaskFailedRecoverable || !task.Retryable {
		t.Fatalf("task=%+v err=%v", task, err)
	}
}

func TestRecoverInterruptedAfterMutationNeedsAttention(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	now := time.Now().UTC()
	database.now = func() time.Time { return now }
	preview := testPreview(now)
	if err := database.CreatePreview(ctx, PreviewInput{
		Preview: preview, ConfirmationHash: HashConfirmation("重启 demo"),
	}); err != nil {
		t.Fatal(err)
	}
	task, _, err := database.StartTask(ctx, "a", model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "重启 demo", IdempotencyKey: "idem-mutated",
	}, "task-mutated")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunningOwned(ctx, task.ID, "restart", "runner-old"); err != nil {
		t.Fatal(err)
	}
	if err := database.MarkProductionChanged(ctx, task.ID, false, "restart"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.RecoverInterrupted(ctx, func(string, string, string, bool) (bool, bool) {
		return false, false
	}); err != nil {
		t.Fatal(err)
	}
	finished, err := database.GetTask(ctx, task.ID)
	if err != nil || finished.State != model.TaskNeedsAttention || finished.Retryable {
		t.Fatalf("task=%+v err=%v", finished, err)
	}
}

func TestReleasePlanApprovalIsDigestBoundAndStartsOnce(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	now := time.Now().UTC()
	database.now = func() time.Time { return now }
	plan := model.ReleasePlan{
		ID: "plan-1", ActorHash: "a", Service: "demo", Action: "update", Target: "v1.2.3",
		Risk: model.RiskHigh, State: model.PlanPendingApproval, Digest: "sha256:abc",
		ConfirmationPhrase: "更新 demo", RequiresConfirmation: true, ObservationSeconds: 300,
		ApprovalSummary: model.ApprovalSummary{
			SchemaVersion: 1, Service: "demo", Action: "update", Target: "v1.2.3",
			Risk: model.RiskHigh, Steps: []string{"preflight", "apply"}, ObservationSeconds: 300,
			ExpectedBefore: map[string]any{"currentVersion": "1.0.0"},
		}, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.CreateReleasePlan(ctx, ReleasePlanInput{
		Plan: plan, ConfirmationHash: HashConfirmation("更新 demo"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ApproveReleasePlan(ctx, plan.ID, "a", "sha256:changed", "更新 demo"); err == nil {
		t.Fatal("changed digest was approved")
	}
	if _, err := database.ApproveReleasePlan(ctx, plan.ID, "a", plan.Digest, "更新 demo"); err != ErrActorMismatch {
		t.Fatalf("creator self-approval err=%v, want ErrActorMismatch", err)
	}
	approved, err := database.ApproveReleasePlan(ctx, plan.ID, "b", plan.Digest, "更新 demo")
	if err != nil || approved.State != model.PlanApproved || approved.ApprovedAt == nil || approved.ApprovedByHash != "b" {
		t.Fatalf("plan=%+v err=%v", approved, err)
	}
	silenceEndsAt := now.Add(20 * time.Minute)
	silence := &model.MaintenanceSilence{ID: "silence-1", EndsAt: silenceEndsAt}
	task, created, err := database.StartPlanTask(ctx, approved, "a", "plan-idem", "plan-task", silence)
	if err != nil || !created || task.PlanID != plan.ID || len(task.Stages) != 2 {
		t.Fatalf("task=%+v created=%v err=%v", task, created, err)
	}
	executing, err := database.GetReleasePlan(ctx, plan.ID)
	if err != nil || executing.MaintenanceSilenceID != silence.ID || executing.MaintenanceSilenceEndsAt == nil {
		t.Fatalf("executing=%+v err=%v", executing, err)
	}
	again, created, err := database.StartPlanTask(ctx, approved, "a", "plan-idem", "other-task", nil)
	if err != nil || created || again.ID != task.ID {
		t.Fatalf("again=%+v created=%v err=%v", again, created, err)
	}
	if err := database.MarkRunningOwned(ctx, task.ID, "preflight", "runner"); err != nil {
		t.Fatal(err)
	}
	event, err := database.CompleteTask(ctx, task.ID, model.TaskSucceeded, "完成", "", "", false, false, "",
		model.Event{TaskID: task.ID, Level: "info", Phase: "terminal", Message: "succeeded",
			Data: map[string]any{"state": model.TaskSucceeded}},
		model.AuditEntry{ActorHash: "a", Event: "task.terminal", Resource: task.ID, Outcome: "succeeded"})
	if err != nil || event.Sequence == 0 {
		t.Fatalf("event=%+v err=%v", event, err)
	}
	observing, err := database.GetReleasePlan(ctx, plan.ID)
	if err != nil || observing.State != model.PlanObserving || observing.ObservationStartedAt == nil ||
		observing.ObservationEndsAt == nil || observing.ClosedAt != nil {
		t.Fatalf("observing=%+v err=%v", observing, err)
	}
	if err := database.RecordPlanSilenceReleased(ctx, plan.ID, model.AuditEntry{
		ActorHash: "a", Event: "plan.maintenance_silence_released", Resource: plan.ID, Outcome: "released",
	}); err != nil {
		t.Fatal(err)
	}
	closeAudit := model.AuditEntry{ActorHash: "a", Event: "plan.closed", Resource: plan.ID, Outcome: "completed"}
	if _, err := database.CloseReleasePlan(ctx, plan.ID, "a", "close-idem", closeAudit); err == nil {
		t.Fatal("observation closed before deadline")
	}
	now = now.Add(301 * time.Second)
	closed, err := database.CloseReleasePlan(ctx, plan.ID, "a", "close-idem", closeAudit)
	if err != nil || closed.State != model.PlanCompleted || closed.ClosedAt == nil {
		t.Fatalf("closed=%+v err=%v", closed, err)
	}
	replayed, err := database.CloseReleasePlan(ctx, plan.ID, "a", "close-idem", closeAudit)
	if err != nil || replayed.State != model.PlanCompleted {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	if _, err := database.CloseReleasePlan(ctx, plan.ID, "a", "other-idem", closeAudit); err != ErrIdempotency {
		t.Fatalf("expected closure idempotency error, got %v", err)
	}
	auditEntries, err := database.ListAudit(ctx, 10, 0)
	if err != nil || len(auditEntries) != 4 || auditEntries[0].Event != "plan.closed" {
		t.Fatalf("audit=%+v err=%v", auditEntries, err)
	}
}

func TestScheduledReleasePlanActivatesOnlyAtScheduleTime(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	database.now = func() time.Time { return now }
	scheduleAt := now.Add(10 * time.Minute)
	plan := model.ReleasePlan{
		ID: "plan-scheduled", ActorHash: "a", Service: "demo", Action: "restart", TenantID: "tenant-a", ServerID: "server-a",
		Risk: model.RiskHigh, State: model.PlanPendingApproval, Digest: "sha256:scheduled", ScheduleAt: &scheduleAt,
		ConfirmationPhrase: "重启 demo", RequiresConfirmation: true,
		ApprovalSummary: model.ApprovalSummary{SchemaVersion: 1, Service: "demo", Action: "restart", TenantID: "tenant-a", ServerID: "server-a",
			Risk: model.RiskHigh, Steps: []string{"preflight", "restart"}, ExpectedBefore: map[string]any{"currentVersion": "1"}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := database.CreateReleasePlan(ctx, ReleasePlanInput{Plan: plan, ConfirmationHash: HashConfirmation(plan.ConfirmationPhrase)}); err != nil {
		t.Fatal(err)
	}
	approved, err := database.ApproveReleasePlan(ctx, plan.ID, "b", plan.Digest, plan.ConfirmationPhrase)
	if err != nil || approved.State != model.PlanScheduled || approved.ApprovedByHash != "b" {
		t.Fatalf("approved=%+v err=%v", approved, err)
	}
	if activated, err := database.ActivateScheduledPlan(ctx, plan.ID, now); err != nil || activated {
		t.Fatalf("early activation=%v err=%v", activated, err)
	}
	due := scheduleAt.Add(time.Second)
	if activated, err := database.ActivateScheduledPlan(ctx, plan.ID, due); err != nil || !activated {
		t.Fatalf("due activation=%v err=%v", activated, err)
	}
	stored, err := database.GetReleasePlan(ctx, plan.ID)
	if err != nil || stored.State != model.PlanApproved || stored.TenantID != "tenant-a" || stored.ServerID != "server-a" || stored.ScheduleAt == nil {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func TestFailedPlanNeedsAttention(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	now := time.Now().UTC()
	database.now = func() time.Time { return now }
	plan := model.ReleasePlan{
		ID: "plan-failed", ActorHash: "a", Service: "demo", Action: "restart",
		Risk: model.RiskMedium, State: model.PlanPendingApproval, Digest: "sha256:failed",
		ConfirmationPhrase: "重启 demo", RequiresConfirmation: true, ObservationSeconds: 60,
		ApprovalSummary: model.ApprovalSummary{
			SchemaVersion: 1, Service: "demo", Action: "restart", Risk: model.RiskMedium,
			Steps: []string{"restart"}, ObservationSeconds: 60, ExpectedBefore: map[string]any{},
		}, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.CreateReleasePlan(ctx, ReleasePlanInput{
		Plan: plan, ConfirmationHash: HashConfirmation("重启 demo"),
	}); err != nil {
		t.Fatal(err)
	}
	approved, err := database.ApproveReleasePlan(ctx, plan.ID, "a", plan.Digest, "重启 demo")
	if err != nil {
		t.Fatal(err)
	}
	task, _, err := database.StartPlanTask(ctx, approved, "a", "failed-idem", "failed-task", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunningOwned(ctx, task.ID, "restart", "runner"); err != nil {
		t.Fatal(err)
	}
	_, err = database.CompleteTask(ctx, task.ID, model.TaskFailedRecoverable, "执行失败", "检查失败",
		"adapter_failed", true, false, "", model.Event{TaskID: task.ID, Level: "error", Phase: "terminal",
			Message: "failed", Data: map[string]any{"state": model.TaskFailedRecoverable}},
		model.AuditEntry{ActorHash: "a", Event: "task.terminal", Resource: task.ID, Outcome: "failed"})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := database.GetReleasePlan(ctx, plan.ID)
	if err != nil || failed.State != model.PlanNeedsAttention || failed.ClosureReason != "检查失败" {
		t.Fatalf("plan=%+v err=%v", failed, err)
	}
}

func TestSnapshotCreatesRestrictedDatabase(t *testing.T) {
	store := openTestStore(t)
	dir := filepath.Join(t.TempDir(), "snapshots")
	path, err := store.Snapshot(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := filepath.Glob(filepath.Join(dir, "*.db"))
	if err != nil || len(info) != 1 || info[0] != path {
		t.Fatalf("snapshot path=%s files=%v err=%v", path, info, err)
	}
}

func TestCollectMetricsIncludesTaskDimensionsAndFinishTime(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 9, 4, 5, 6, 0, time.UTC)
	store.now = func() time.Time { return now }
	preview := testPreview(now)
	if err := store.CreatePreview(ctx, PreviewInput{
		Preview: preview, ConfirmationHash: HashConfirmation("重启 demo"),
	}); err != nil {
		t.Fatal(err)
	}
	request := model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "重启 demo", IdempotencyKey: "metrics-task",
	}
	if _, _, err := store.StartTask(ctx, "a", request, "task-metrics"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(ctx, "task-metrics", "restart"); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishTask(ctx, "task-metrics", model.TaskSucceeded, "完成", ""); err != nil {
		t.Fatal(err)
	}

	metrics, err := store.CollectMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.TasksByState[model.TaskSucceeded] != 1 || len(metrics.TasksByService) != 1 {
		t.Fatalf("unexpected task metrics: %+v", metrics)
	}
	series := metrics.TasksByService[0]
	if series.Service != "demo" || series.Action != "restart" || series.State != model.TaskSucceeded || series.Count != 1 {
		t.Fatalf("unexpected task series: %+v", series)
	}
	if len(metrics.LastFinishedTasks) != 1 || metrics.LastFinishedTasks[0].FinishedEpoch != float64(now.Unix()) {
		t.Fatalf("unexpected finish metrics: %+v", metrics.LastFinishedTasks)
	}
}

func TestCollectMetricsIncludesActiveCredentialRotationAge(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	now := time.Date(2026, 8, 16, 4, 5, 6, 0, time.UTC)
	started := now.Add(-25 * time.Hour)
	database.now = func() time.Time { return started }
	rotation := model.CredentialRotation{
		ID: "metrics-rotation", IdempotencyKey: "metrics-rotation-key",
		ActorHash: strings.Repeat("a", 64), CredentialType: model.GitHubAlertmanagerCredential,
		Target: "fixed target", State: model.CredentialRotationRunning,
		Fingerprint: "sha256:0123456789ab", ExpiresAt: "2027-08-12", CreatedAt: started,
	}
	if _, _, err := database.StartCredentialRotation(ctx, rotation); err != nil {
		t.Fatal(err)
	}
	if err := database.FinishCredentialRotation(ctx, rotation.ID, model.CredentialRotationResult{
		State: model.CredentialRotationSwitchedPendingRevocation,
	}); err != nil {
		t.Fatal(err)
	}
	database.now = func() time.Time { return now }
	metrics, err := database.CollectMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics.ActiveCredentialRotations) != 1 {
		t.Fatalf("unexpected credential metrics: %+v", metrics.ActiveCredentialRotations)
	}
	item := metrics.ActiveCredentialRotations[0]
	if item.CredentialType != model.GitHubAlertmanagerCredential ||
		item.State != model.CredentialRotationSwitchedPendingRevocation ||
		item.AgeSeconds != (25*time.Hour).Seconds() {
		t.Fatalf("unexpected credential metric: %+v", item)
	}
}

func TestPruneRemovesExpiredPreviewDetailButRetainsTaskSummary(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	now := time.Date(2026, 8, 9, 4, 5, 6, 0, time.UTC)
	database.now = func() time.Time { return now }
	preview := testPreview(now)
	if err := database.CreatePreview(ctx, PreviewInput{
		Preview: preview, ConfirmationHash: HashConfirmation("重启 demo"),
	}); err != nil {
		t.Fatal(err)
	}
	request := model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "重启 demo", IdempotencyKey: "retention-task",
	}
	if _, _, err := database.StartTask(ctx, "a", request, "task-retention"); err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunning(ctx, "task-retention", "restart"); err != nil {
		t.Fatal(err)
	}
	if err := database.FinishTask(ctx, "task-retention", model.TaskSucceeded, "完成", ""); err != nil {
		t.Fatal(err)
	}

	database.now = func() time.Time { return now.Add(31 * 24 * time.Hour) }
	if err := database.Prune(ctx, 30*24*time.Hour, 365*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	var previews, tasks int
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM previews`).Scan(&previews); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if previews != 0 || tasks != 1 {
		t.Fatalf("previews=%d tasks=%d", previews, tasks)
	}
	if _, err := database.GetTask(ctx, "task-retention"); err != nil {
		t.Fatalf("retained task summary is unreadable: %v", err)
	}
}

func TestDiscoveryRollbackSourceAndPagination(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	base := time.Date(2026, 8, 11, 5, 0, 0, 0, time.UTC)
	database.now = func() time.Time { return base }

	createTask := func(id, action, state string, createdAt time.Time) model.Task {
		t.Helper()
		database.now = func() time.Time { return createdAt }
		preview := testPreview(createdAt)
		preview.ID = "preview-" + id
		preview.Action = action
		if err := database.CreatePreview(ctx, PreviewInput{
			Preview: preview, ConfirmationHash: HashConfirmation("重启 demo"),
		}); err != nil {
			t.Fatal(err)
		}
		task, _, err := database.StartTask(ctx, "a", model.StartTaskRequest{
			PreviewID: preview.ID, Confirmation: "重启 demo", IdempotencyKey: "idem-" + id,
		}, "task-"+id)
		if err != nil {
			t.Fatal(err)
		}
		if state != string(model.TaskQueued) {
			if err := database.MarkRunning(ctx, task.ID, action); err != nil {
				t.Fatal(err)
			}
			if err := database.FinishTask(ctx, task.ID, model.TaskState(state), "完成", ""); err != nil {
				t.Fatal(err)
			}
		}
		return task
	}

	check := createTask("check", "check", string(model.TaskSucceeded), base)
	if _, err := database.AppendEvent(ctx, model.Event{
		TaskID: check.ID, Level: "info", Phase: "discover", Message: "发现完成",
		Data: map[string]any{"latestTag": "v1.2.3", "prepared": false},
	}); err != nil {
		t.Fatal(err)
	}
	discovery, found, err := database.LatestSuccessfulDiscovery(ctx, "demo")
	if err != nil || !found || discovery["latestTag"] != "v1.2.3" || discovery["prepared"] != false {
		t.Fatalf("discovery=%v found=%v err=%v", discovery, found, err)
	}

	update := createTask("update", "update", string(model.TaskSucceeded), base.Add(time.Minute))
	source, found, err := database.LatestSucceededUpdate(ctx, "demo")
	if err != nil || !found || source.ID != update.ID {
		t.Fatalf("source=%+v found=%v err=%v", source, found, err)
	}
	createTask("queued", "inspect", string(model.TaskQueued), base.Add(2*time.Minute))
	first, err := database.ListTasks(ctx, 2, 0)
	if err != nil || len(first) != 2 || first[0].ID != "task-queued" || first[1].ID != update.ID {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := database.ListTasks(ctx, 2, 2)
	if err != nil || len(second) != 1 || second[0].ID != check.ID {
		t.Fatalf("second=%+v err=%v", second, err)
	}

	for index := 0; index < 3; index++ {
		if _, err := database.AppendAudit(ctx, model.AuditEntry{
			ActorHash: "a", Event: "test", Resource: "item", Outcome: "accepted",
		}); err != nil {
			t.Fatal(err)
		}
	}
	audit, err := database.ListAudit(ctx, 2, 2)
	if err != nil || len(audit) != 1 || audit[0].Sequence != 1 {
		t.Fatalf("audit=%+v err=%v", audit, err)
	}

	events, err := database.ListTaskEvents(ctx, check.ID, 0, 1)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	after, err := database.ListTaskEvents(ctx, check.ID, events[0].Sequence, 1)
	if err != nil || len(after) != 0 {
		t.Fatalf("after=%+v err=%v", after, err)
	}
	if err := database.MarkRunning(ctx, "task-queued", "inspect"); err != nil {
		t.Fatal(err)
	}
	if err := database.FinishTask(ctx, "task-queued", model.TaskSucceeded, "完成", ""); err != nil {
		t.Fatal(err)
	}

	prepare := createTask("prepare", "prepare", string(model.TaskSucceeded), base.Add(3*time.Minute))
	if _, err := database.db.ExecContext(ctx, `UPDATE tasks SET target = 'v1.2.3' WHERE id = ?`, prepare.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AppendEvent(ctx, model.Event{
		TaskID: prepare.ID, Level: "info", Phase: "publish", Message: "准备完成",
		Data: map[string]any{"tag": "v1.2.3", "status": "prepared"},
	}); err != nil {
		t.Fatal(err)
	}
	discovery, found, err = database.LatestSuccessfulDiscovery(ctx, "demo")
	if err != nil || !found || discovery["prepared"] != true {
		t.Fatalf("prepared discovery=%v found=%v err=%v", discovery, found, err)
	}
}
