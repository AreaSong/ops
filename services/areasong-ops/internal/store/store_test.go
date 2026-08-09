package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
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
	count, err := store.RecoverInterrupted(ctx)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	task, err := store.GetTask(ctx, "task-3")
	if err != nil || task.State != model.TaskRecoveryUncertain {
		t.Fatalf("task=%+v err=%v", task, err)
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
