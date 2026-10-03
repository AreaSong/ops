package runner

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestScheduledPlanRequestBindingAndEarlyExecution(t *testing.T) {
	engine, _ := testEngine(t, &fakeExecutor{})
	ctx := context.Background()
	at := time.Now().UTC().Add(time.Hour)
	request := model.PreviewRequest{Service: "demo", Action: "restart", ScheduleAt: &at, IdempotencyKey: mustUUID(t)}
	plan, err := engine.CreateReleasePlan(ctx, actorHash(), request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ScheduleAt == nil || !plan.ScheduleAt.Equal(at) || plan.ApprovalSummary.ScheduleAt == nil || !plan.ApprovalSummary.ScheduleAt.Equal(at) {
		t.Fatalf("schedule not bound: %+v", plan)
	}
	replay, err := engine.CreateReleasePlan(ctx, actorHash(), request)
	if err != nil || replay.ID != plan.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	later := at.Add(time.Hour)
	request.ScheduleAt = &later
	if _, err = engine.CreateReleasePlan(ctx, actorHash(), request); !errors.Is(err, store.ErrIdempotency) {
		t.Fatalf("changed schedule reused key: %v", err)
	}
	request.IdempotencyKey = mustUUID(t)
	changed, err := engine.CreateReleasePlan(ctx, actorHash(), request)
	if err != nil || changed.Digest == plan.Digest {
		t.Fatalf("schedule not in digest: %v", err)
	}
	approved := approveReleasePlanForTest(t, engine, plan, actorHash())
	if approved.State != model.PlanScheduled {
		t.Fatalf("state=%s", approved.State)
	}
	if _, _, err = engine.ExecuteReleasePlan(ctx, actorHash(), plan.ID, model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)}); err == nil || !strings.Contains(err.Error(), "尚未到达调度时间") {
		t.Fatalf("early execution=%v", err)
	}
	if _, _, err = engine.ExecuteReleasePlan(ctx, strings.Repeat("b", 64), plan.ID, model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)}); !errors.Is(err, store.ErrActorMismatch) {
		t.Fatalf("wrong executor=%v", err)
	}
}

func TestDueScheduledPlanExplicitExecutionReplay(t *testing.T) {
	engine, _ := testEngine(t, &fakeExecutor{})
	ctx := context.Background()
	// 使用已到期时间与隔离存储构造 scheduled 状态；不等待真实时钟或改运行态代码。
	at := time.Now().UTC().Add(-time.Minute)
	plan, err := engine.CreateReleasePlan(ctx, actorHash(), model.PreviewRequest{Service: "demo", Action: "restart", ScheduleAt: &at})
	if err != nil {
		t.Fatal(err)
	}
	plan = approveReleasePlanForTest(t, engine, plan, actorHash())
	raw, err := sql.Open("sqlite", filepath.Join(engine.stateRoot, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, "UPDATE release_plans SET state = ? WHERE id = ?", model.PlanScheduled, plan.ID); err != nil {
		t.Fatal(err)
	}
	request := model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)}
	task, created, err := engine.ExecuteReleasePlan(ctx, actorHash(), plan.ID, request)
	if err != nil || !created {
		t.Fatalf("execute: created=%v err=%v", created, err)
	}
	engine.Wait()
	replay, created, err := engine.ExecuteReleasePlan(ctx, actorHash(), plan.ID, request)
	if err != nil || created || replay.ID != task.ID {
		t.Fatalf("replay: task=%+v created=%v err=%v", replay, created, err)
	}
}
