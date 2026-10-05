package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestScheduledPlanRequestBindingAndEarlyExecution(t *testing.T) {
	engine, _ := preparationEngine(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(time.Hour)
	request := model.PreviewRequest{Service: "demo", Action: "update", Target: "v1.1.0", ScheduleAt: &at, IdempotencyKey: mustUUID(t)}
	plan, err := engine.createManualReleasePlan(ctx, actorHash(), request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ScheduleAt == nil || !plan.ScheduleAt.Equal(at) || plan.ApprovalSummary.ScheduleAt == nil || !plan.ApprovalSummary.ScheduleAt.Equal(at) {
		t.Fatalf("schedule not bound: %+v", plan)
	}
	replay, err := engine.createManualReleasePlan(ctx, actorHash(), request)
	if err != nil || replay.ID != plan.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	later := at.Add(time.Hour)
	request.ScheduleAt = &later
	if _, err = engine.createManualReleasePlan(ctx, actorHash(), request); !errors.Is(err, store.ErrIdempotency) {
		t.Fatalf("changed schedule reused key: %v", err)
	}
	request.IdempotencyKey = mustUUID(t)
	changed, err := engine.createManualReleasePlan(ctx, actorHash(), request)
	if err != nil || changed.Digest == plan.Digest {
		t.Fatalf("schedule not in digest: %v", err)
	}
	approved := approveReleasePlanForTest(t, engine, plan, strings.Repeat("b", 64))
	if approved.State != model.PlanScheduled {
		t.Fatalf("state=%s", approved.State)
	}
	if _, _, err = engine.ExecuteReleasePlan(ctx, actorHash(), plan.ID, model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)}); err == nil {
		t.Fatalf("early execution=%v", err)
	}
	if _, _, err = engine.ExecuteReleasePlan(ctx, strings.Repeat("b", 64), plan.ID, model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)}); !errors.Is(err, store.ErrActorMismatch) {
		t.Fatalf("wrong executor=%v", err)
	}
}

func TestDueScheduledPlanExplicitExecutionReplay(t *testing.T) {
	engine, _ := testEngine(t, &fakeExecutor{})
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Minute)
	plan := historicalRunnerPlan(t, engine, actorHash(), model.PreviewRequest{Service: "demo", Action: "restart", ScheduleAt: &at})
	raw := historicalRunnerDB(t, engine)
	if _, err := raw.Exec("UPDATE release_plans SET state=?,approved_by_hash=? WHERE id=?", model.PlanScheduled, actorHash(), plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.ExecuteReleasePlan(ctx, actorHash(), plan.ID, model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)}); !errors.Is(err, model.ErrReleaseNotIntegrated) {
		t.Fatal("到期激活绕过B-P", err)
	}
	before, err := engine.store.GetReleasePlan(ctx, plan.ID)
	if err != nil || before.State != model.PlanScheduled {
		t.Fatal("拒绝后仍激活计划", err)
	}
	task := seedHistoricalRunnerTask(t, engine, model.Task{ActorHash: actorHash(), Service: plan.Service, Action: plan.Action, PlanID: plan.ID, PlanDigest: plan.Digest, State: model.TaskSucceeded})
	if _, err := raw.Exec("UPDATE release_plans SET state=?,task_id=? WHERE id=?", model.PlanCompleted, task.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	replay, created, err := engine.ExecuteReleasePlan(ctx, actorHash(), plan.ID, model.ExecutePlanRequest{IdempotencyKey: task.IdempotencyKey})
	if err != nil || created || replay.ID != task.ID {
		t.Fatal("历史任务不能安全重放", err)
	}
	if len(engine.executor.(*fakeExecutor).calls) != 0 {
		t.Fatal("历史重放调用适配器")
	}
}
