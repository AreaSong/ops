package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"strings"
	"testing"
	"time"
)

func registeredFixture(t *testing.T) (*Store, model.ReleasePlan, ReleaseWorkInput) {
	t.Helper()
	db, _ := lifecycleFixture(t)
	ctx := context.Background()
	snap, _, _ := db.GetAccessPolicySnapshot(ctx)
	var policy config.AccessPolicy
	json.Unmarshal([]byte(snap.PolicyJSON), &policy)
	p := policy.Principals[lifecycleApprover]
	p.Roles = []string{"platform-admin"}
	policy.Principals[lifecycleApprover] = p
	raw, _ := json.Marshal(policy)
	_, err := db.SaveAccessPolicySnapshot(ctx, model.AccessPolicySnapshot{ActorHash: lifecycleActor, PolicyJSON: string(raw), Digest: "sha256:" + model.WorkDigest(string(raw))}, snap.Version)
	if err != nil {
		t.Fatal(err)
	}
	r, in := inspectedPreparation(t, db, "registered")
	_, err = db.FinishPreparedReleasePlan(ctx, preparationOwner(r), &in)
	if err != nil {
		t.Fatal(err)
	}
	a := r.Request.Authority
	approver := a
	approver.ActorHash = lifecycleApprover
	approver.TenantID = "team"
	plan, err := db.ApprovePreparedReleasePlan(ctx, in.Plan.ID, approver, model.ApprovePlanRequest{Digest: in.Plan.Digest})
	if err != nil {
		t.Fatal(err)
	}
	targets, _ := plan.ApprovalSummary.Lifecycle.WorkTargets()
	input := ReleaseWorkInput{Authority: a, Admission: WorkAdmissionInput{AdmissionID: "release-admission", OwnerToken: strings.Repeat("f", 64), Request: model.WorkAdmissionRequest{Version: 1, Kind: model.WorkAdmissionKind, WorkID: plan.ID, IdempotencyKey: "execution-key", ActorHash: a.ActorHash, ApprovalDigest: plan.Digest, Targets: targets}}}
	return db, plan, input
}
func releaseMutation(r model.WorkAdmission, input ReleaseWorkInput) WorkAdmissionMutation {
	return WorkAdmissionMutation{ID: r.ID, Kind: r.Request.Kind, WorkID: r.Request.WorkID, IdempotencyKey: r.Request.IdempotencyKey, ActorHash: r.Request.ActorHash, ApprovalDigest: r.Request.ApprovalDigest, RequestDigest: r.RequestDigest, OwnerToken: input.Admission.OwnerToken, ExpectedState: r.State, ExpectedRevision: r.Revision, TaskID: r.TaskID}
}
func registeredPrepared(t *testing.T, db *Store, input ReleaseWorkInput) model.WorkAdmission {
	t.Helper()
	ctx := context.Background()
	r, created, err := db.AdmitReleaseWork(ctx, input)
	if err != nil || !created {
		t.Fatal(err)
	}
	r, advanced, err := db.BeginReleaseWorkPreparation(ctx, releaseMutation(r, input), input.Authority)
	if err != nil || !advanced {
		t.Fatal(err)
	}
	return r
}
func registeredBound(t *testing.T, db *Store, input ReleaseWorkInput) model.WorkAdmission {
	t.Helper()
	r := registeredPrepared(t, db, input)
	_, r, err := db.StartRegisteredPlanTaskWithEvent(context.Background(), RegisteredPlanTaskInput{Mutation: releaseMutation(r, input), Authority: input.Authority, TaskID: "registered-task"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func registeredTerminal(t *testing.T, db *Store, input ReleaseWorkInput) model.WorkAdmission {
	t.Helper()
	r := registeredBound(t, db, input)
	ctx := context.Background()
	m := releaseMutation(r, input)
	if err := db.MarkRegisteredRunningOwned(ctx, m, "preflight", "runner", input.Authority); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CompleteRegisteredTask(ctx, RegisteredTaskCompletion{Mutation: m, State: model.TaskSucceeded}); err != nil {
		t.Fatal(err)
	}
	return r
}
func releaseClose(r model.WorkAdmission, input ReleaseWorkInput) RegisteredPlanClose {
	return RegisteredPlanClose{Work: WorkAdmissionClose{Mutation: releaseMutation(r, input), CloseKind: "settled", Evidence: model.WorkCloseEvidence{Kind: "execution_and_cleanup_settled_v1", ExecutorStopped: true, CleanupConfirmed: true, ResultDigest: strings.Repeat("a", 64), CleanupDigest: strings.Repeat("b", 64)}}, Authority: input.Authority, IdempotencyKey: "closure-key"}
}
func TestRegisteredStoreRejectsBypass(t *testing.T) {
	for _, field := range []string{"targets", "digest", "actor", "unapproved", "proof", "summary"} {
		t.Run(field, func(t *testing.T) {
			db, plan, input := registeredFixture(t)
			switch field {
			case "targets":
				input.Admission.Request.Targets = input.Admission.Request.Targets[:1]
			case "digest":
				input.Admission.Request.ApprovalDigest = strings.Repeat("c", 64)
			case "actor":
				input.Authority.ActorHash = lifecycleApprover
			case "unapproved":
				lifecycleSQL(t, db, `UPDATE release_plans SET approved_by_hash=''`)
			case "proof":
				lifecycleSQL(t, db, `UPDATE release_plan_preparations SET result_json='{}'`)
			case "summary":
				lifecycleSQL(t, db, `UPDATE release_plans SET approval_summary_json='{}'`)
			}
			before := lifecycleContents(t, db)
			if _, created, err := db.AdmitReleaseWork(context.Background(), input); err == nil || created {
				t.Fatal("bypass accepted")
			}
			assertLifecycleUnchanged(t, db, before)
			if _, _, err := db.StartPlanTask(context.Background(), plan, lifecycleActor, "new", "task", nil); err == nil {
				t.Fatal("legacy start accepted")
			}
		})
	}
}
func TestRegisteredStoreAtomicFailures(t *testing.T) {
	for _, point := range []string{"admit", "begin", "task", "plan", "event", "audit", "bind", "running", "terminal", "close", "close-audit", "commit-queue", "commit-close"} {
		t.Run(point, func(t *testing.T) {
			db, _, input := registeredFixture(t)
			ctx := context.Background()
			var r model.WorkAdmission
			switch point {
			case "admit":
			case "begin":
				r, _, _ = db.AdmitReleaseWork(ctx, input)
			case "running", "terminal":
				r = registeredBound(t, db, input)
			case "close", "close-audit", "commit-close":
				r = registeredTerminal(t, db, input)
			default:
				r = registeredPrepared(t, db, input)
			}
			clauses := map[string]string{"admit": "INSERT ON work_admissions", "begin": "UPDATE ON work_admissions", "task": "INSERT ON tasks", "plan": "UPDATE ON release_plans", "event": "INSERT ON events", "audit": "INSERT ON audit_entries", "bind": "UPDATE ON work_admissions", "running": "UPDATE ON tasks", "terminal": "UPDATE ON release_plans", "close": "UPDATE ON work_admissions", "close-audit": "INSERT ON audit_entries"}
			if strings.HasPrefix(point, "commit") {
				lifecycleSQL(t, db, `CREATE TABLE release_commit_fault(ref TEXT REFERENCES tenants(id) DEFERRABLE INITIALLY DEFERRED)`)
				lifecycleSQL(t, db, `CREATE TRIGGER br_fault AFTER UPDATE ON work_admissions BEGIN INSERT INTO release_commit_fault VALUES('absent'); END`)
			} else {
				lifecycleSQL(t, db, "CREATE TRIGGER br_fault BEFORE "+clauses[point]+" BEGIN SELECT RAISE(ABORT,'synthetic release fault'); END")
			}
			before := lifecycleContents(t, db)
			var err error
			switch point {
			case "admit":
				_, _, err = db.AdmitReleaseWork(ctx, input)
			case "begin":
				var advanced bool
				_, advanced, err = db.BeginReleaseWorkPreparation(ctx, releaseMutation(r, input), input.Authority)
				if advanced {
					t.Fatal("commit failure granted authority")
				}
			case "running":
				err = db.MarkRegisteredRunningOwned(ctx, releaseMutation(r, input), "preflight", "runner", input.Authority)
			case "terminal":
				_, err = db.CompleteRegisteredTask(ctx, RegisteredTaskCompletion{Mutation: releaseMutation(r, input), State: model.TaskSucceeded})
			case "close", "close-audit", "commit-close":
				_, err = db.ClosePlanAndRegisteredWork(ctx, releaseClose(r, input))
			default:
				var started TaskStartResult
				started, _, err = db.StartRegisteredPlanTaskWithEvent(ctx, RegisteredPlanTaskInput{Mutation: releaseMutation(r, input), Authority: input.Authority, TaskID: "task"})
				if started.Created {
					t.Fatal("failed transaction created task")
				}
			}
			if err == nil {
				t.Fatal("fault did not fire")
			}
			assertLifecycleUnchanged(t, db, before)
			assertLifecycleUnchanged(t, secondWorkStore(t, db), before)
		})
	}
}
func TestRegisteredStoreLifecycleRace(t *testing.T) {
	for _, firstKind := range []string{"admit", "disable", "close"} {
		t.Run(firstKind, func(t *testing.T) {
			db, _, input := registeredFixture(t)
			ctx := context.Background()
			var r model.WorkAdmission
			if firstKind == "close" {
				r = registeredTerminal(t, db, input)
			}
			snapshot, _, _ := db.GetAccessPolicySnapshot(ctx)
			execution := approveLifecycle(t, db, lifecycleRequest(snapshot.Version, 1, "disabled"))
			second := secondWorkStore(t, db)
			finish := holdWorkWriter(t, db, func() error {
				switch firstKind {
				case "admit":
					_, _, e := db.AdmitReleaseWork(ctx, input)
					return e
				case "disable":
					_, e := db.applyTenantLifecycleChange(ctx, execution)
					return e
				default:
					_, e := db.ClosePlanAndRegisteredWork(ctx, releaseClose(r, input))
					return e
				}
			})
			_, err := second.applyTenantLifecycleChange(ctx, execution)
			requireWorkBusy(t, err)
			if err = finish(); err != nil {
				t.Fatal(err)
			}
			if firstKind == "admit" {
				if _, err = second.applyTenantLifecycleChange(ctx, execution); err == nil {
					t.Fatal("admitted work failed to block")
				}
			}
			if firstKind == "disable" {
				if _, _, err = second.AdmitReleaseWork(ctx, input); err == nil {
					t.Fatal("disabled tenant admitted")
				}
			}
			if firstKind == "close" {
				if _, err = second.applyTenantLifecycleChange(ctx, execution); err != nil {
					t.Fatal("closed work still blocks", err)
				}
			}
			t.Log(fmt.Sprintf("%s serialized on same writer boundary", firstKind))
		})
	}
}

func TestRegisteredStoreScheduledAndAdmissionOwnership(t *testing.T) {
	db, plan, input := registeredFixture(t)
	ctx := context.Background()
	// 此夹具直接重建带时刻的创建证明与摘要，随后重新通过真实批准入口。
	at := db.now().Add(time.Hour)
	prep, _, err := db.GetPlanPreparationByRequest(ctx, plan.RequestIdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	prep.Request.ScheduleAt = &at
	raw, digest, _, err := model.CanonicalPlanPreparation(prep.Request)
	if err != nil {
		t.Fatal(err)
	}
	summary := plan.ApprovalSummary
	summary.ScheduleAt = &at
	summaryRaw, _ := json.Marshal(summary)
	approval, _ := model.ReleaseApprovalDigest(summary)
	lifecycleSQL(t, db, `UPDATE release_plan_preparations SET request_json=?,request_digest=? WHERE id=?`, raw, digest, prep.ID)
	lifecycleSQL(t, db, `UPDATE release_plans SET approval_summary_json=?,digest=?,schedule_at=?,state='pending_approval',approved_by_hash='',approved_at=NULL WHERE id=?`, string(summaryRaw), approval, timeText(at), plan.ID)
	approver := input.Authority
	approver.ActorHash = lifecycleApprover
	approver.TenantID = "team"
	plan, err = db.ApprovePreparedReleasePlan(ctx, plan.ID, approver, model.ApprovePlanRequest{Digest: approval})
	if err != nil {
		t.Fatal(err)
	}
	if plan.State != model.PlanScheduled {
		t.Fatal(plan.State)
	}
	input.Admission.Request.ApprovalDigest = approval
	if _, _, err = db.AdmitReleaseWork(ctx, input); err == nil {
		t.Fatal("early admission")
	}
	db.now = func() time.Time { return at }
	r, created, err := db.AdmitReleaseWork(ctx, input)
	if err != nil || !created {
		t.Fatal(err)
	}
	m := releaseMutation(r, input)
	wrong := m
	wrong.OwnerToken = strings.Repeat("0", 64)
	if _, advanced, e := db.BeginReleaseWorkPreparation(ctx, wrong, input.Authority); e == nil || advanced {
		t.Fatal("wrong owner began")
	}
	r, advanced, err := db.BeginReleaseWorkPreparation(ctx, m, input.Authority)
	if err != nil || !advanced {
		t.Fatal(err)
	}
	if _, advanced, e := db.BeginReleaseWorkPreparation(ctx, m, input.Authority); e == nil || advanced {
		t.Fatal("same revision regranted")
	}
	started, _, err := db.StartRegisteredPlanTaskWithEvent(ctx, RegisteredPlanTaskInput{Mutation: releaseMutation(r, input), Authority: input.Authority, TaskID: "scheduled-task"})
	if err != nil || !started.Created {
		t.Fatal(err)
	}
	stored, _ := db.GetReleasePlan(ctx, plan.ID)
	if stored.State != model.PlanExecuting {
		t.Fatal(stored.State)
	}
}

func TestRegisteredClosureBlockerAtomicAndRevocation(t *testing.T) {
	for _, point := range []string{"plan", "audit", "commit", "revoked-authority"} {
		t.Run(point, func(t *testing.T) {
			db, _, input := registeredFixture(t)
			ctx := context.Background()
			r := registeredTerminal(t, db, input)
			blocker := RegisteredClosureBlocker{Mutation: releaseMutation(r, input), Authority: input.Authority, IdempotencyKey: "blocked-attempt", Reason: "synthetic alert", Fingerprints: []string{"0123456789abcdef"}}
			if point == "revoked-authority" {
				snapshot, _, err := db.GetAccessPolicySnapshot(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var policy config.AccessPolicy
				if err = json.Unmarshal([]byte(snapshot.PolicyJSON), &policy); err != nil {
					t.Fatal(err)
				}
				principal := policy.Principals[lifecycleActor]
				principal.Status = "disabled"
				policy.Principals[lifecycleActor] = principal
				raw, _ := json.Marshal(policy)
				second := secondWorkStore(t, db)
				if _, err = second.SaveAccessPolicySnapshot(ctx, model.AccessPolicySnapshot{ActorHash: lifecycleApprover, PolicyJSON: string(raw), Digest: "sha256:" + model.WorkDigest(string(raw))}, snapshot.Version); err != nil {
					t.Fatal(err)
				}
			} else if point == "commit" {
				lifecycleSQL(t, db, `CREATE TABLE br_blocker_commit(ref TEXT REFERENCES tenants(id) DEFERRABLE INITIALLY DEFERRED)`)
				lifecycleSQL(t, db, `CREATE TRIGGER br_blocker_fault AFTER UPDATE ON release_plans BEGIN INSERT INTO br_blocker_commit VALUES('absent'); END`)
			} else {
				clause := "UPDATE ON release_plans"
				if point == "audit" {
					clause = "INSERT ON audit_entries"
				}
				lifecycleSQL(t, db, "CREATE TRIGGER br_blocker_fault BEFORE "+clause+" BEGIN SELECT RAISE(ABORT,'blocker fault'); END")
			}
			before := lifecycleContents(t, db)
			if err := db.RecordRegisteredClosureBlocker(ctx, blocker); err == nil {
				t.Fatal("故障或陈旧authority仍签发阻断凭据")
			}
			assertLifecycleUnchanged(t, db, before)
			assertLifecycleUnchanged(t, secondWorkStore(t, db), before)
		})
	}
}
