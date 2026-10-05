package runner

import (
	"context"
	"errors"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRegisteredConcurrentOnce(t *testing.T) {
	e, p, m := brEngine(t, 60)
	plan := brApproved(t, e, bpRequest(t))
	p.syntheticInspection.entered = make(chan struct{})
	p.syntheticInspection.release = make(chan struct{})
	key := mustUUID(t)
	done := make(chan error, 1)
	go func() {
		_, _, err := e.ExecuteReleasePlan(context.Background(), actorHash(), plan.ID, model.ExecutePlanRequest{IdempotencyKey: key})
		done <- err
	}()
	<-p.syntheticInspection.entered
	_, created, err := e.ExecuteReleasePlan(context.Background(), actorHash(), plan.ID, model.ExecutePlanRequest{IdempotencyKey: key})
	if err == nil || created {
		t.Fatal("second execution authority")
	}
	close(p.syntheticInspection.release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	e.Wait()
	if p.calls != 2 || m.creates != 1 || len(p.phases) != 3 {
		t.Fatal("duplicate calls")
	}
	var count int
	brDB(t, e).QueryRow(`SELECT count(*) FROM tasks WHERE plan_id=?`, plan.ID).Scan(&count)
	if count != 1 {
		t.Fatal("duplicate task")
	}
}
func TestRegisteredExecutionFaultsRemainBlocked(t *testing.T) {
	for _, point := range []string{"inspect", "cleanup", "silence", "queue", "terminal", "owner", "confirm", "silence-record", "close-commit"} {
		t.Run(point, func(t *testing.T) {
			e, p, m := brEngine(t, 60)
			plan := brApproved(t, e, bpRequest(t))
			db := brDB(t, e)
			switch point {
			case "inspect":
				p.failedResult = true
			case "cleanup":
				e.inspectionCleanup = func(string) error { return errors.New("cleanup failed") }
			case "silence":
				m.unknown = true
			case "queue":
				db.Exec(`CREATE TRIGGER br_fault BEFORE INSERT ON tasks BEGIN SELECT RAISE(ABORT,'queue fault'); END`)
			case "terminal":
				p.after = func(in ExecuteInput) {
					if in.Phase == "health" {
						db.Exec(`CREATE TRIGGER br_fault BEFORE UPDATE ON tasks WHEN NEW.finished_at IS NOT NULL BEGIN SELECT RAISE(ABORT,'terminal fault'); END`)
					}
				}
			}
			task, created, err := e.ExecuteReleasePlan(context.Background(), actorHash(), plan.ID, model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)})
			e.Wait()
			if point == "inspect" || point == "cleanup" || point == "silence" || point == "queue" {
				if err == nil || created {
					t.Fatal("fault granted task")
				}
				var count int
				db.QueryRow(`SELECT count(*) FROM work_admissions WHERE state<>'closed'`).Scan(&count)
				if count != 1 {
					t.Fatal("lost blocker")
				}
				if len(p.phases) != 0 {
					t.Fatal("run after failed queue")
				}
				return
			}
			if err != nil || !created {
				t.Fatal(err)
			}
			if point == "terminal" {
				var state string
				db.QueryRow(`SELECT state FROM work_admissions`).Scan(&state)
				if state == model.WorkClosed {
					t.Fatal("terminal fault closed")
				}
				return
			}
			brReady(t, e, plan)
			switch point {
			case "owner":
				e.releaseMu.Lock()
				delete(e.releases, task.ID)
				e.releaseMu.Unlock()
			case "confirm":
				m.notExpired = true
			case "silence-record":
				db.Exec(`CREATE TRIGGER br_fault BEFORE UPDATE ON release_plans WHEN NEW.maintenance_silence_released_at IS NOT NULL BEGIN SELECT RAISE(ABORT,'release record fault'); END`)
			case "close-commit":
				db.Exec(`CREATE TABLE br_commit(ref TEXT REFERENCES tenants(id) DEFERRABLE INITIALLY DEFERRED)`)
				db.Exec(`CREATE TRIGGER br_fault AFTER UPDATE ON work_admissions WHEN NEW.state='closed' BEGIN INSERT INTO br_commit VALUES('absent'); END`)
			}
			_, err = e.CloseReleasePlan(context.Background(), actorHash(), plan.ID, model.ClosePlanRequest{IdempotencyKey: mustUUID(t)})
			if err == nil {
				t.Fatal("fault closed")
			}
			var blocked *planClosureBlocked
			if errors.As(err, &blocked) {
				t.Fatal("unknown effects grant new attempt")
			}
			brState(t, e, task, model.PlanObserving, model.WorkTaskBound)
			if point == "silence-record" && (m.deletes != 1 || m.gets != 2) {
				t.Fatal("repeated delete")
			}
		})
	}
}
func TestRegisteredWrongOwnerBeforeDirectory(t *testing.T) {
	e, p, _ := brEngine(t, 60)
	plan := brApproved(t, e, bpRequest(t))
	ctx := context.Background()
	a, _ := e.releaseAuthority(ctx, actorHash(), "service:demo")
	targets, _ := plan.ApprovalSummary.Lifecycle.WorkTargets()
	owner, _ := model.NewWorkOwnerToken()
	r, _, err := e.store.AdmitReleaseWork(ctx, store.ReleaseWorkInput{Authority: a, Admission: store.WorkAdmissionInput{AdmissionID: mustUUID(t), OwnerToken: owner, Request: model.WorkAdmissionRequest{Version: 1, Kind: model.WorkAdmissionKind, WorkID: plan.ID, IdempotencyKey: mustUUID(t), ActorHash: actorHash(), ApprovalDigest: plan.Digest, Targets: targets}}})
	if err != nil {
		t.Fatal(err)
	}
	r, _, err = e.store.BeginWorkPreparation(ctx, workMutation(r, owner))
	if err != nil {
		t.Fatal(err)
	}
	started, r, err := e.store.StartRegisteredPlanTaskWithEvent(ctx, store.RegisteredPlanTaskInput{Mutation: workMutation(r, owner), Authority: a, TaskID: mustUUID(t)})
	if err != nil {
		t.Fatal(err)
	}
	l := &releaseExecutionLease{record: r, owner: strings.Repeat("f", 64), taskID: started.Task.ID, plan: plan, executor: p, attempts: make(map[string]closureAttempt)}
	if err = e.insertReleaseLease(l); err != nil {
		t.Fatal(err)
	}
	e.runRegistered(started.Task, l)
	if _, err = os.Stat(filepath.Join(e.stateRoot, "operations", started.Task.ID)); !os.IsNotExist(err) {
		t.Fatal("wrong owner created directory")
	}
	task, _ := e.store.GetTask(ctx, started.Task.ID)
	if task.State != model.TaskQueued || len(p.phases) != 0 {
		t.Fatal("wrong owner failed another task")
	}
	copy := *l
	copy.owner = owner
	if e.insertReleaseLease(&copy) == nil {
		t.Fatal("owner overwritten")
	}
}
func TestRegisteredLateCallNoRollbackOrClose(t *testing.T) {
	e, p, _ := brEngine(t, 0)
	service := e.catalog.Services["demo"]
	action := service.Actions["update"]
	action.TimeoutSeconds = 1
	service.Actions["update"] = action
	e.catalog.Services["demo"] = service
	p.late = make(chan struct{})
	lateReady := make(chan struct{})
	lateWrite := make(chan struct{})
	lateDone := make(chan struct{})
	p.after = func(in ExecuteInput) {
		go func() {
			close(lateReady)
			<-lateWrite
			os.WriteFile(filepath.Join(in.OperationDir, "late-evidence"), []byte("late"), 0600)
			close(p.late)
			close(lateDone)
		}()
	}
	plan := brApproved(t, e, bpRequest(t))
	task, _ := brExecute(t, e, plan)
	<-lateReady
	e.Wait()
	brState(t, e, task, model.PlanNeedsAttention, model.WorkUncertain)
	if len(p.phases) != 1 {
		t.Fatal("overlapping rollback")
	}
	close(lateWrite)
	<-lateDone
	brState(t, e, task, model.PlanNeedsAttention, model.WorkUncertain)
	if len(p.phases) != 1 {
		t.Fatal("late callback revived work")
	}
}
func TestRegisteredLoopbackResponseLoss(t *testing.T) {
	e, p, m := brEngine(t, 60)
	plan := brApproved(t, e, bpRequest(t))
	handler := NewServer(e, e.store)
	drop := true
	accepted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if drop {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, r)
			drop = false
			close(accepted)
			connection, _, _ := w.(http.Hijacker).Hijack()
			connection.Close()
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	key := mustUUID(t)
	body := `{"idempotencyKey":"` + key + `"}`
	request, _ := http.NewRequest("POST", server.URL+"/v1/plans/"+plan.ID+"/execute", strings.NewReader(body))
	request.Header.Set(actorHeader, actorHash())
	request.Header.Set("Content-Type", "application/json")
	if response, err := http.DefaultClient.Do(request); err == nil {
		response.Body.Close()
		t.Fatal("response not lost")
	}
	<-accepted
	e.Wait()
	request, _ = http.NewRequest("POST", server.URL+"/v1/plans/"+plan.ID+"/execute", strings.NewReader(body))
	request.Header.Set(actorHeader, actorHash())
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.Status)
	}
	if p.calls != 2 || len(p.phases) != 3 || m.creates != 1 {
		t.Fatal("response loss repeated effects")
	}
}
func TestRegisteredScheduledManualOnly(t *testing.T) {
	e, p, m := brEngine(t, 60)
	request := bpRequest(t)
	at := time.Now().Add(time.Hour)
	request.ScheduleAt = &at
	plan := brApproved(t, e, request)
	key := mustUUID(t)
	_, _, err := e.ExecuteReleasePlan(context.Background(), actorHash(), plan.ID, model.ExecutePlanRequest{IdempotencyKey: key})
	if err == nil {
		t.Fatal("early schedule")
	}
	if p.calls != 1 || m.creates != 0 {
		t.Fatal("early effects")
	}
	// 两端可控时间通过原持久化计划时间与原摘要一致性测试覆盖到期；不改已批准摘要。
	for i := 0; i < 2; i++ {
		post := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/plans/"+plan.ID, nil)
		req.Header.Set(actorHeader, actorHash())
		NewServer(e, e.store).ServeHTTP(post, req)
	}
	if p.calls != 1 || m.creates != 0 {
		t.Fatal("refresh executed")
	}
}

func TestRegisteredSilenceLocalCommitRecovery(t *testing.T) {
	e, _, m := brEngine(t, 60)
	p := brApproved(t, e, bpRequest(t))
	task, _ := brExecute(t, e, p)
	e.Wait()
	brReady(t, e, p)
	db := brDB(t, e)
	_, err := db.Exec(`CREATE TRIGGER br_release_fault BEFORE UPDATE ON release_plans WHEN NEW.maintenance_silence_released_at IS NOT NULL BEGIN SELECT RAISE(ABORT,'local record fault'); END`)
	if err != nil {
		t.Fatal(err)
	}
	m.afterGet = func(count int) {
		if count == 2 {
			if _, err := db.Exec(`DROP TRIGGER br_release_fault`); err != nil {
				t.Error(err)
			}
		}
	}
	_, err = e.CloseReleasePlan(context.Background(), actorHash(), p.ID, model.ClosePlanRequest{IdempotencyKey: mustUUID(t)})
	if err != nil {
		t.Fatal(err)
	}
	brState(t, e, task, model.PlanCompleted, model.WorkClosed)
	if m.deletes != 1 || m.gets != 2 {
		t.Fatal("local recovery repeated external write")
	}
	var count int
	db.QueryRow(`SELECT count(*) FROM audit_entries WHERE event='plan.maintenance_silence_released'`).Scan(&count)
	if count != 1 {
		t.Fatal("release audit count", count)
	}
}
func TestRegisteredCloseResponseLoss(t *testing.T) {
	e, p, m := brEngine(t, 60)
	plan := brApproved(t, e, bpRequest(t))
	task, _ := brExecute(t, e, plan)
	e.Wait()
	brReady(t, e, plan)
	handler := NewServer(e, e.store)
	drop := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if drop {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, r)
			if recorder.Code != 200 {
				t.Error(recorder.Body.String())
			}
			drop = false
			connection, _, _ := w.(http.Hijacker).Hijack()
			connection.Close()
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	key := mustUUID(t)
	for i := 0; i < 2; i++ {
		request, _ := http.NewRequest("POST", server.URL+"/v1/plans/"+plan.ID+"/close", strings.NewReader(`{"idempotencyKey":"`+key+`"}`))
		request.Header.Set(actorHeader, actorHash())
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if i == 0 {
			if err == nil {
				response.Body.Close()
				t.Fatal("close response not lost")
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatal(response.Status)
			}
		}
	}
	brState(t, e, task, model.PlanCompleted, model.WorkClosed)
	if p.calls != 3 || len(p.phases) != 3 || m.deletes != 1 || m.gets != 1 {
		t.Fatal("closure response loss repeated activity")
	}
}
func TestRegisteredZeroObservationIntermediateState(t *testing.T) {
	e, p, _ := brEngine(t, 0)
	plan := brApproved(t, e, bpRequest(t))
	db := brDB(t, e)
	_, err := db.Exec(`CREATE TABLE br_states(state TEXT); CREATE TRIGGER br_states_record AFTER UPDATE ON release_plans WHEN NEW.state<>OLD.state BEGIN INSERT INTO br_states VALUES(NEW.state); END`)
	if err != nil {
		t.Fatal(err)
	}
	task, _ := brExecute(t, e, plan)
	e.Wait()
	brState(t, e, task, model.PlanCompleted, model.WorkClosed)
	rows, err := db.Query(`SELECT state FROM br_states ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var states []string
	for rows.Next() {
		var state string
		rows.Scan(&state)
		states = append(states, state)
	}
	if strings.Join(states, ",") != "executing,observing,completed" || p.calls != 3 {
		t.Fatalf("states=%v inspect=%d", states, p.calls)
	}
}
