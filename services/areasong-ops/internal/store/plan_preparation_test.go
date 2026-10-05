package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func planPreparationInput(t *testing.T, db *Store, key string) PlanPreparationInput {
	t.Helper()
	snapshot, found, err := db.GetAccessPolicySnapshot(context.Background())
	if err != nil || !found {
		t.Fatal(err)
	}
	authority := model.ReleaseAuthority{ObjectID: "service:synthetic", ActorHash: lifecycleActor, TenantID: "default", Permission: model.PermissionDeploy, PolicyVersion: snapshot.Version, PolicyDigest: snapshot.Digest}
	binding, err := db.CaptureReleaseLifecycle(context.Background(), authority, model.ReleaseLifecycleBinding{Version: 1, Source: "manual_single", ExecutionMode: "local",
		PreparationID: "preparation-" + key, CreatorTenantID: "default", ScopeDigest: strings.Repeat("d", 64),
		TargetObjects: []model.ReleaseTargetObject{{ObjectID: "service:other", TenantID: "other", ServerID: "local"}, {ObjectID: "service:synthetic", TenantID: "team", ServerID: "local"}}})
	if err != nil {
		t.Fatal(err)
	}
	return PlanPreparationInput{ID: binding.PreparationID, OwnerToken: strings.Repeat("c", 64), Request: model.PlanPreparationRequest{Version: 1, Kind: model.PlanPreparationKind,
		PlanID: "plan-" + key, IdempotencyKey: key, InputDigest: strings.Repeat("e", 64), Authority: authority, Service: "synthetic", Action: "update", Target: "v1.2.0", Binding: binding}}
}

func preparationOwner(r model.PlanPreparation) PlanPreparationMutation {
	return PlanPreparationMutation{ID: r.ID, PlanID: r.Request.PlanID, IdempotencyKey: r.Request.IdempotencyKey, ActorHash: r.Request.Authority.ActorHash,
		RequestDigest: r.RequestDigest, ScopeDigest: r.Request.Binding.ScopeDigest, OwnerToken: strings.Repeat("c", 64), ExpectedState: r.State, ExpectedRevision: r.Revision}
}

func inspectedPreparation(t *testing.T, db *Store, key string) (model.PlanPreparation, ReleasePlanInput) {
	t.Helper()
	ctx := context.Background()
	r, created, err := db.AdmitPlanPreparation(ctx, planPreparationInput(t, db, key))
	if err != nil || !created {
		t.Fatal(err)
	}
	r, advanced, err := db.BeginPlanInspection(ctx, preparationOwner(r))
	if err != nil || !advanced {
		t.Fatal(err)
	}
	r, err = db.RecordPlanInspectionDone(ctx, preparationOwner(r), model.PlanInspectionResult{Kind: "inspection_settled_v1", ScopeDigest: r.Request.Binding.ScopeDigest,
		Succeeded: true, Snapshot: map[string]any{"currentVersion": "1.0.0"}, CallDigests: []string{strings.Repeat("a", 64)}, CleanupDigest: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	s := model.ApprovalSummary{SchemaVersion: 2, ApprovalPolicy: model.ApprovalPolicyTwoParty, Service: r.Request.Service, Action: r.Request.Action, Target: r.Request.Target,
		TenantID: "team", ServerID: "local", Risk: model.RiskHigh, Steps: []string{"preflight"}, ExpectedBefore: map[string]any{"currentVersion": "1.0.0"}, Lifecycle: &r.Request.Binding}
	digest, err := model.ReleaseApprovalDigest(s)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	plan := model.ReleasePlan{ID: r.Request.PlanID, ActorHash: lifecycleActor, Service: s.Service, Action: s.Action, Target: s.Target, TenantID: s.TenantID, ServerID: s.ServerID,
		Risk: s.Risk, State: model.PlanPendingApproval, Digest: digest, ApprovalSummary: s, ApprovalPolicy: s.ApprovalPolicy, RequiresDualApproval: true,
		RequestIdempotencyKey: r.Request.IdempotencyKey, RequestDigest: r.Request.InputDigest, CreatedAt: now, UpdatedAt: now}
	return r, ReleasePlanInput{Plan: plan, ConfirmationHash: HashConfirmation("")}
}

func TestPlanPreparationAtomicPlanAndLifecycle(t *testing.T) {
	db, _ := lifecycleFixture(t)
	ctx := context.Background()
	r, plan := inspectedPreparation(t, db, "success")
	execution := approveLifecycle(t, db, lifecycleRequest(1, 1, "disabled"))
	before := lifecycleContents(t, db)
	if _, err := db.applyTenantLifecycleChange(ctx, execution); !errors.Is(err, ErrUnfinishedRegisteredWork) {
		t.Fatal(err)
	}
	assertLifecycleUnchanged(t, db, before)
	m := preparationOwner(r)
	closed, err := db.FinishPreparedReleasePlan(ctx, m, &plan)
	if err != nil || closed.State != model.WorkClosed || closed.ProducedPlanID != plan.Plan.ID {
		t.Fatal(err)
	}
	stored, err := db.GetReleasePlan(ctx, plan.Plan.ID)
	if err != nil || !sameReleaseJSON(stored.ApprovalSummary, plan.Plan.ApprovalSummary) {
		t.Fatal(err)
	}
	before = lifecycleContents(t, db)
	if _, advanced, e := db.BeginPlanInspection(ctx, m); e == nil || advanced {
		t.Fatal("迟到调用复活closed")
	}
	if _, e := db.MarkPlanPreparationUncertain(ctx, m); e == nil {
		t.Fatal("迟到回调覆盖closed")
	}
	assertLifecycleUnchanged(t, db, before)
	if _, err = db.FinishPreparedReleasePlan(ctx, m, &plan); err != nil {
		t.Fatal(err)
	}
	assertLifecycleUnchanged(t, db, before)
	if _, err = db.applyTenantLifecycleChange(ctx, execution); err != nil {
		t.Fatal(err)
	}
	enable := approveLifecycle(t, db, lifecycleRequest(2, 2, "active"))
	if _, err = db.applyTenantLifecycleChange(ctx, enable); err != nil {
		t.Fatal(err)
	}
	snap, _, _ := db.GetAccessPolicySnapshot(ctx)
	authority := model.ReleaseAuthority{ObjectID: "service:synthetic", ActorHash: lifecycleApprover, TenantID: "team", Permission: model.PermissionDeploy, PolicyVersion: snap.Version, PolicyDigest: snap.Digest}
	if _, err = db.ApprovePreparedReleasePlan(ctx, plan.Plan.ID, authority, model.ApprovePlanRequest{Digest: stored.Digest}); err == nil {
		t.Fatal("旧代次获新批准")
	}
	after, _ := db.GetReleasePlan(ctx, stored.ID)
	if !sameReleaseJSON(after, stored) {
		t.Fatal("拒绝后改写历史计划")
	}
}

func TestPlanPreparationOwnerReplayAndUncertainty(t *testing.T) {
	db, _ := lifecycleFixture(t)
	ctx := context.Background()
	input := planPreparationInput(t, db, "owner")
	r, created, err := db.AdmitPlanPreparation(ctx, input)
	if err != nil || !created {
		t.Fatal(err)
	}
	before := lifecycleContents(t, db)
	input.OwnerToken = strings.Repeat("f", 64)
	got, created, err := db.AdmitPlanPreparation(ctx, input)
	if err != nil || created || got.ID != r.ID {
		t.Fatal(err)
	}
	assertLifecycleUnchanged(t, db, before)
	m := preparationOwner(r)
	m.OwnerToken = input.OwnerToken
	if _, advanced, err := db.BeginPlanInspection(ctx, m); err == nil || advanced {
		t.Fatal("重放接管owner")
	}
	m = preparationOwner(r)
	r, advanced, err := db.BeginPlanInspection(ctx, m)
	if err != nil || !advanced {
		t.Fatal(err)
	}
	if _, advanced, err = db.BeginPlanInspection(ctx, m); err == nil || advanced {
		t.Fatal("重复执行权")
	}
	if _, err = db.ClosePlanPreparationNoWork(ctx, preparationOwner(r)); err == nil {
		t.Fatal("preparing被释放")
	}
	r, err = db.MarkPlanPreparationUncertain(ctx, preparationOwner(r))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.FinishPreparedReleasePlan(ctx, preparationOwner(r), nil); err == nil {
		t.Fatal("uncertain被释放")
	}
	reopened := secondWorkStore(t, db)
	record, err := reopened.GetPlanPreparation(ctx, r.ID)
	if err != nil || record.State != model.WorkUncertain {
		t.Fatal(err)
	}
	data, _ := json.Marshal(m)
	request, _ := json.Marshal(r.Request)
	if strings.Contains(string(data), strings.Repeat("c", 64)) || strings.Contains(string(request), `"ownerToken"`) {
		t.Fatal("owner序列化泄漏")
	}
}

func TestPlanPreparationRejectsTargetsAndForgery(t *testing.T) {
	for _, mutation := range []string{"generation", "target", "actor", "policy", "input", "scope", "version"} {
		t.Run(mutation, func(t *testing.T) {
			db, _ := lifecycleFixture(t)
			ctx := context.Background()
			input := planPreparationInput(t, db, mutation)
			switch mutation {
			case "generation":
				input.Request.Binding.Targets[0].ExpectedGeneration = "0"
			case "target":
				input.Request.Binding.TargetObjects[0].TenantID = "missing"
			case "actor":
				input.Request.Authority.ActorHash = lifecycleApprover
			case "policy":
				input.Request.Authority.PolicyVersion++
			case "input":
				input.Request.InputDigest = "sha256:" + strings.Repeat("a", 64)
			case "scope":
				input.Request.Binding.ScopeDigest = ""
			case "version":
				input.Request.Version = 2
			}
			before := lifecycleContents(t, db)
			if _, created, err := db.AdmitPlanPreparation(ctx, input); err == nil || created {
				t.Fatal("无效登记成功")
			}
			assertLifecycleUnchanged(t, db, before)
		})
	}
	db, _ := lifecycleFixture(t)
	r, p := inspectedPreparation(t, db, "forged")
	before := lifecycleContents(t, db)
	if err := db.CreateReleasePlan(context.Background(), p); err == nil {
		t.Fatal("v2绕过创建检查")
	}
	p.Plan.ApprovalSummary.Lifecycle.ScopeDigest = strings.Repeat("f", 64)
	if _, err := db.FinishPreparedReleasePlan(context.Background(), preparationOwner(r), &p); err == nil {
		t.Fatal("换scope被接受")
	}
	assertLifecycleUnchanged(t, db, before)
}

func TestPlanPreparationKnownFailureAndNoWork(t *testing.T) {
	db, _ := lifecycleFixture(t)
	ctx := context.Background()
	r, _, err := db.AdmitPlanPreparation(ctx, planPreparationInput(t, db, "no-work"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ClosePlanPreparationNoWork(ctx, preparationOwner(r)); err != nil {
		t.Fatal(err)
	}
	r, _, err = db.AdmitPlanPreparation(ctx, planPreparationInput(t, db, "failure"))
	if err != nil {
		t.Fatal(err)
	}
	r, _, err = db.BeginPlanInspection(ctx, preparationOwner(r))
	if err != nil {
		t.Fatal(err)
	}
	result := model.PlanInspectionResult{Kind: "inspection_settled_v1", ScopeDigest: r.Request.Binding.ScopeDigest, Failure: "已确认的检查拒绝", CallDigests: []string{}, CleanupDigest: strings.Repeat("d", 64)}
	r, err = db.RecordPlanInspectionDone(ctx, preparationOwner(r), result)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := db.FinishPreparedReleasePlan(ctx, preparationOwner(r), nil)
	if err != nil || closed.ProducedPlanID != "" {
		t.Fatal(err)
	}
	if _, err = db.GetReleasePlan(ctx, r.Request.PlanID); !errors.Is(err, ErrNotFound) {
		t.Fatal("失败检查伪造计划")
	}
}
