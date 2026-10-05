package runner

import (
	"context"
	"encoding/json"
	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func saveReleasePolicy(t *testing.T, e *Engine, change func(*config.AccessPolicy)) {
	t.Helper()
	policy, snapshot, err := e.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	change(policy)
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Open(filepath.Join(e.stateRoot, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_, err = second.SaveAccessPolicySnapshot(context.Background(), model.AccessPolicySnapshot{ActorHash: strings.Repeat("b", 64), PolicyJSON: string(raw), Digest: "sha256:" + model.WorkDigest(string(raw))}, snapshot.Version)
	if err != nil {
		t.Fatal(err)
	}
}
func TestRegisteredRunRevokedSecondaryTarget(t *testing.T) {
	e, profile, _ := brEngine(t, 60)
	for _, name := range []string{"demo", "other"} {
		service := e.catalog.Services[name]
		service.TenantID = "default"
		e.catalog.Services[name] = service
	}
	saveReleasePolicy(t, e, func(policy *config.AccessPolicy) {
		p := policy.Principals[actorHash()]
		p.Roles = nil
		policy.Principals[actorHash()] = p
		policy.Roles["deployer"] = model.Role{ID: "deployer", Permissions: []model.Permission{model.PermissionDeploy, model.PermissionRead}}
		policy.Bindings = append(policy.Bindings, model.RoleBinding{ID: "release-targets", Subject: actorHash(), TenantID: "default", RoleID: "deployer", ObjectIDs: []string{"service:demo", "service:other"}})
	})
	plan := brApproved(t, e, bpRequest(t))
	ctx := context.Background()
	a, err := e.releaseAuthority(ctx, actorHash(), "service:demo")
	if err != nil {
		t.Fatal(err)
	}
	targets, _ := plan.ApprovalSummary.Lifecycle.WorkTargets()
	owner, _ := model.NewWorkOwnerToken()
	r, _, err := e.store.AdmitReleaseWork(ctx, store.ReleaseWorkInput{Authority: a, Admission: store.WorkAdmissionInput{AdmissionID: mustUUID(t), OwnerToken: owner, Request: model.WorkAdmissionRequest{Version: 1, Kind: model.WorkAdmissionKind, WorkID: plan.ID, IdempotencyKey: mustUUID(t), ActorHash: actorHash(), ApprovalDigest: plan.Digest, Targets: targets}}})
	if err != nil {
		t.Fatal(err)
	}
	r, _, err = e.store.BeginReleaseWorkPreparation(ctx, workMutation(r, owner), a)
	if err != nil {
		t.Fatal(err)
	}
	started, r, err := e.store.StartRegisteredPlanTaskWithEvent(ctx, store.RegisteredPlanTaskInput{Mutation: workMutation(r, owner), Authority: a, TaskID: mustUUID(t)})
	if err != nil {
		t.Fatal(err)
	}
	l := &releaseExecutionLease{record: r, owner: owner, taskID: started.Task.ID, plan: plan, executor: profile, attempts: make(map[string]closureAttempt)}
	if err = e.insertReleaseLease(l); err != nil {
		t.Fatal(err)
	}
	gate, done := make(chan struct{}), make(chan struct{})
	go func() { <-gate; e.runRegistered(started.Task, l); close(done) }()
	saveReleasePolicy(t, e, func(policy *config.AccessPolicy) {
		for i := range policy.Bindings {
			if policy.Bindings[i].ID == "release-targets" {
				policy.Bindings[i].ObjectIDs = []string{"service:demo"}
			}
		}
	})
	if err = e.authorize(ctx, actorHash(), model.PermissionDeploy, "service:demo"); err != nil {
		t.Fatal("主服务权限应保留", err)
	}
	if err = e.authorize(ctx, actorHash(), model.PermissionDeploy, "service:other"); err == nil {
		t.Fatal("次级目标撤权未生效")
	}
	close(gate)
	<-done
	task, err := e.store.GetTask(ctx, started.Task.ID)
	if err != nil || task.State != model.TaskQueued {
		t.Fatal("错误改变他人任务", err)
	}
	if _, err = os.Stat(filepath.Join(e.stateRoot, "operations", task.ID)); !os.IsNotExist(err) {
		t.Fatal("撤权后创建目录")
	}
	if len(profile.phases) != 0 || profile.calls != 1 {
		t.Fatal("撤权后调用执行器")
	}
	brState(t, e, task, model.PlanExecuting, model.WorkTaskBound)
}
func TestRegisteredClosureRevocationDoesNotIssueAttempt(t *testing.T) {
	e, profile, m := brEngine(t, 60)
	plan := brApproved(t, e, bpRequest(t))
	task, _ := brExecute(t, e, plan)
	e.Wait()
	brReady(t, e, plan)
	profile.syntheticInspection.entered = make(chan struct{})
	profile.syntheticInspection.release = make(chan struct{})
	profile.failedResult = true
	key := mustUUID(t)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- postPreparation(t, e, actorHash(), "/v1/plans/"+plan.ID+"/close", model.ClosePlanRequest{IdempotencyKey: key})
	}()
	<-profile.syntheticInspection.entered
	saveReleasePolicy(t, e, func(policy *config.AccessPolicy) {
		p := policy.Principals[actorHash()]
		p.Status = "disabled"
		policy.Principals[actorHash()] = p
	})
	close(profile.syntheticInspection.release)
	response := <-done
	if response.Code < 400 || strings.Contains(response.Body.String(), "newAttemptAllowed") || strings.Contains(response.Body.String(), "attemptState") {
		t.Fatal("撤权后颁发新尝试", response.Body.String())
	}
	var count int
	brDB(t, e).QueryRow(`SELECT count(*) FROM audit_entries WHERE event='plan.close_rejected'`).Scan(&count)
	if count != 0 {
		t.Fatal("撤权后写结束阻断审计")
	}
	if m.deletes != 1 || m.gets != 1 || profile.calls != 3 {
		t.Fatal("外部调用不匹配")
	}
	brState(t, e, task, model.PlanObserving, model.WorkTaskBound)
}

func TestRegisteredClosureAuditFailureNoAttempt(t *testing.T) {
	e, profile, m := brEngine(t, 60)
	plan := brApproved(t, e, bpRequest(t))
	task, _ := brExecute(t, e, plan)
	e.Wait()
	brReady(t, e, plan)
	db := brDB(t, e)
	_, err := db.Exec(`CREATE TRIGGER br_block_audit BEFORE INSERT ON audit_entries WHEN NEW.event='plan.close_rejected' BEGIN SELECT RAISE(ABORT,'blocked audit failed'); END`)
	if err != nil {
		t.Fatal(err)
	}
	m.blocked = true
	response := postPreparation(t, e, actorHash(), "/v1/plans/"+plan.ID+"/close", model.ClosePlanRequest{IdempotencyKey: mustUUID(t)})
	if response.Code < 400 || strings.Contains(response.Body.String(), "newAttemptAllowed") {
		t.Fatal("审计失败返回新键资格", response.Body.String())
	}
	response = postPreparation(t, e, actorHash(), "/v1/plans/"+plan.ID+"/close", model.ClosePlanRequest{IdempotencyKey: mustUUID(t)})
	if response.Code < 400 || strings.Contains(response.Body.String(), "newAttemptAllowed") {
		t.Fatal("未结束尝试被新键覆盖")
	}
	if m.deletes != 1 || m.gets != 1 || profile.calls != 2 {
		t.Fatal("新键重复调用")
	}
	brState(t, e, task, model.PlanObserving, model.WorkTaskBound)
}
