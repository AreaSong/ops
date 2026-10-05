package runner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type syntheticRelease struct {
	*syntheticInspection
	mu               sync.Mutex
	phases           []string
	version          string
	fail             string
	entered, release chan struct{}
	late             chan struct{}
	after            func(ExecuteInput)
}

func (s *syntheticRelease) resolveReleaseExecutionScope(context.Context, model.ServiceDefinition, model.ActionDefinition, releaseScopeInput) (model.ReleaseScopeDefinition, error) {
	return s.scope, nil
}
func (s *syntheticRelease) executePlanInspection(ctx context.Context, in ExecuteInput) inspectionCall {
	call := s.syntheticInspection.executePlanInspection(ctx, in)
	s.mu.Lock()
	version := s.version
	s.mu.Unlock()
	if version != "" {
		call.Result.Data["currentVersion"] = version
	}
	return call
}
func (s *syntheticRelease) executeReleaseCall(ctx context.Context, in ExecuteInput) inspectionCall {
	s.mu.Lock()
	s.phases = append(s.phases, in.Phase)
	s.mu.Unlock()
	if s.entered != nil {
		close(s.entered)
		s.entered = nil
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return inspectionCall{Err: ctx.Err()}
		}
	}
	if s.after != nil {
		s.after(in)
	}
	done := make(chan struct{})
	close(done)
	if s.late != nil {
		done = s.late
	}
	err := error(nil)
	if in.Phase == s.fail || (s.fail == "rollback" && in.Phase == "apply") {
		err = errors.New("synthetic phase failure")
	}
	if err == nil && in.Phase == "apply" {
		s.mu.Lock()
		s.version = "1.1.0"
		s.mu.Unlock()
	}
	return inspectionCall{Result: model.AdapterResult{OK: err == nil, Summary: "合成执行 " + in.Phase, Data: map[string]any{}}, Err: err, Settled: done, EvidenceDigest: model.WorkDigest("execution:" + in.OperationDir + ":" + in.Phase)}
}

type loopbackSilence struct {
	mu                                    sync.Mutex
	creates, deletes, gets                int
	expired, blocked, unknown, notExpired bool
	afterDelete                           func()
	afterGet                              func(int)
}

func (m *loopbackSilence) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/api/v2/alerts":
		if m.blocked {
			json.NewEncoder(w).Encode([]map[string]any{{"fingerprint": "0123456789abcdef", "labels": map[string]string{"service": "demo", "alertname": "AppHttpProbeFailed", "severity": "critical"}, "status": map[string]any{"state": "active"}}})
		} else {
			w.Write([]byte(`[]`))
		}
	case r.Method == "POST" && r.URL.Path == "/api/v2/silences":
		m.creates++
		if m.unknown {
			w.Write([]byte(`{}`))
			return
		}
		w.Write([]byte(`{"silenceID":"br-exact"}`))
	case r.Method == "DELETE" && r.URL.Path == "/api/v2/silence/br-exact":
		m.deletes++
		m.expired = true
		if m.afterDelete != nil {
			m.afterDelete()
		}
		w.Write([]byte(`{}`))
	case r.Method == "GET" && r.URL.Path == "/api/v2/silence/br-exact":
		m.gets++
		if m.afterGet != nil {
			m.afterGet(m.gets)
		}
		state := "active"
		if m.expired && !m.notExpired {
			state = "expired"
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "br-exact", "status": map[string]string{"state": state}})
	default:
		http.Error(w, "unexpected", 500)
	}
}
func brEngine(t *testing.T, seconds int) (*Engine, *syntheticRelease, *loopbackSilence) {
	engine, inspection := preparationEngine(t)
	profile := &syntheticRelease{syntheticInspection: inspection}
	engine.executor = profile
	service := engine.catalog.Services["demo"]
	action := service.Actions["update"]
	action.Steps = []string{"preflight", "apply", "health"}
	action.ObservationSeconds = seconds
	action.PhaseSemantics = map[string]model.PhaseSemantics{"apply": {Effect: "runtime_mutation", FailurePolicy: "rollback", RecoveryPhase: "rollback"}, "health": {Effect: "observe", FailurePolicy: "rollback", RecoveryPhase: "rollback"}}
	service.Actions["update"] = action
	engine.catalog.Services["demo"] = service
	manager := &loopbackSilence{}
	server := httptest.NewServer(manager)
	t.Cleanup(server.Close)
	client, err := NewAlertmanagerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	engine.alertmanager = client
	return engine, profile, manager
}
func brApproved(t *testing.T, engine *Engine, request model.PreviewRequest) model.ReleasePlan {
	t.Helper()
	ctx := context.Background()
	plan, err := engine.createManualReleasePlan(ctx, actorHash(), request)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = engine.ApproveReleasePlan(ctx, strings.Repeat("b", 64), plan.ID, model.ApprovePlanRequest{Digest: plan.Digest, Confirmation: plan.ConfirmationPhrase})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
func brExecute(t *testing.T, e *Engine, p model.ReleasePlan) (model.Task, string) {
	t.Helper()
	key := mustUUID(t)
	task, created, err := e.ExecuteReleasePlan(context.Background(), actorHash(), p.ID, model.ExecutePlanRequest{IdempotencyKey: key})
	if err != nil || !created {
		t.Fatalf("execute created=%v err=%v", created, err)
	}
	return task, key
}
func brDB(t *testing.T, e *Engine) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(e.stateRoot, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func brState(t *testing.T, e *Engine, task model.Task, wantPlan model.PlanState, wantWork string) {
	t.Helper()
	p, err := e.store.GetReleasePlan(context.Background(), task.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	var state string
	if err = brDB(t, e).QueryRow(`SELECT state FROM work_admissions WHERE work_id=?`, p.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if p.State != wantPlan || state != wantWork {
		actual, _ := e.store.GetTask(context.Background(), task.ID)
		t.Fatalf("plan=%s work=%s task=%s error=%s", p.State, state, actual.State, actual.Error)
	}
}
func brReady(t *testing.T, e *Engine, p model.ReleasePlan) {
	t.Helper()
	db := brDB(t, e)
	_, err := db.Exec(`UPDATE release_plans SET observation_ends_at=? WHERE id=?`, time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), p.ID)
	if err != nil {
		t.Fatal(err)
	}
}
func TestRegisteredReleaseLifecycle(t *testing.T) {
	e, profile, m := brEngine(t, 60)
	p := brApproved(t, e, bpRequest(t))
	task, key := brExecute(t, e, p)
	e.Wait()
	brState(t, e, task, model.PlanObserving, model.WorkTaskBound)
	if len(profile.phases) != 3 || profile.calls != 2 || m.creates != 1 {
		t.Fatalf("phase=%v inspection=%d creates=%d", profile.phases, profile.calls, m.creates)
	}
	replay, created, err := e.ExecuteReleasePlan(context.Background(), actorHash(), p.ID, model.ExecutePlanRequest{IdempotencyKey: key})
	if err != nil || created || replay.ID != task.ID {
		t.Fatal("replay", err)
	}
	brReady(t, e, p)
	m.blocked = true
	closeKey := mustUUID(t)
	_, err = e.CloseReleasePlan(context.Background(), actorHash(), p.ID, model.ClosePlanRequest{IdempotencyKey: closeKey})
	var blocked *planClosureBlocked
	if !errors.As(err, &blocked) || blocked.AttemptID != closeKey {
		t.Fatal("blocked", err)
	}
	brState(t, e, task, model.PlanObserving, model.WorkTaskBound)
	m.blocked = false
	_, err = e.CloseReleasePlan(context.Background(), actorHash(), p.ID, model.ClosePlanRequest{IdempotencyKey: closeKey})
	if !errors.As(err, &blocked) {
		t.Fatal("same-key must replay", err)
	}
	finalKey := mustUUID(t)
	_, err = e.CloseReleasePlan(context.Background(), actorHash(), p.ID, model.ClosePlanRequest{IdempotencyKey: finalKey})
	if err != nil {
		t.Fatal(err)
	}
	brState(t, e, task, model.PlanCompleted, model.WorkClosed)
	_, err = e.CloseReleasePlan(context.Background(), actorHash(), p.ID, model.ClosePlanRequest{IdempotencyKey: finalKey})
	if err != nil {
		t.Fatal(err)
	}
	if m.deletes != 1 || m.gets != 1 || profile.calls != 3 || len(profile.phases) != 3 {
		t.Fatalf("duplicate effects: %+v calls=%d", m, profile.calls)
	}
	l := e.releaseLease(task.ID)
	if !l.heartbeatStopped || !l.locksReleased {
		t.Fatal("resources not stopped")
	}
	contract, err := os.ReadFile(filepath.Join(e.stateRoot, "operations", task.ID, "task-contract.json"))
	if err != nil || strings.Contains(string(contract), l.owner) {
		t.Fatal("owner contract leak", err)
	}
	if _, err = e.store.CheckRegisteredTask(context.Background(), l.mutation()); err == nil {
		t.Fatal("late capability revived")
	}
	t.Log("create→approve→prepare→inspect→silence→atomic queue→run→observing→blocked→new attempt→silence confirmed→closed; counts exact")
}
func TestRegisteredReleaseZeroAndFailure(t *testing.T) {
	for _, failure := range []string{"", "preflight", "apply", "rollback"} {
		t.Run("failure="+failure, func(t *testing.T) {
			e, profile, _ := brEngine(t, 0)
			profile.fail = failure
			p := brApproved(t, e, bpRequest(t))
			task, _ := brExecute(t, e, p)
			e.Wait()
			if failure == "preflight" || failure == "apply" {
				brState(t, e, task, model.PlanNeedsAttention, model.WorkClosed)
			} else if failure == "rollback" {
				brState(t, e, task, model.PlanNeedsAttention, model.WorkTaskBound)
			} else {
				brState(t, e, task, model.PlanCompleted, model.WorkClosed)
			}
			var count int
			if err := brDB(t, e).QueryRow(`SELECT count(*) FROM events WHERE task_id=? AND phase='terminal'`, task.ID).Scan(&count); err != nil || count != 1 {
				t.Fatal("terminal", count, err)
			}
		})
	}
}
func TestRegisteredReleaseRejectsChangedContract(t *testing.T) {
	for _, change := range []string{"scope", "implementation", "generation", "digest", "default", "remote", "target"} {
		t.Run(change, func(t *testing.T) {
			e, profile, m := brEngine(t, 60)
			p := brApproved(t, e, bpRequest(t))
			switch change {
			case "scope":
				service := e.catalog.Services["other"]
				service.ServerID = "changed"
				e.catalog.Services["other"] = service
			case "implementation":
				for path := range profile.scope.ImplementationDigests {
					os.WriteFile(path, []byte("changed"), 0600)
				}
			case "generation":
				brDB(t, e).Exec(`UPDATE tenants SET lifecycle_generation=2 WHERE id='team'`)
			case "digest":
				brDB(t, e).Exec(`UPDATE release_plans SET digest=? WHERE id=?`, strings.Repeat("f", 64), p.ID)
			case "target":
				brDB(t, e).Exec(`UPDATE release_plans SET target='v2.0.0' WHERE id=?`, p.ID)
			case "default":
				e.executor = CommandExecutor{}
			case "remote":
				e.remoteDispatch = true
			}
			_, _, err := e.ExecuteReleasePlan(context.Background(), actorHash(), p.ID, model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)})
			if err == nil {
				t.Fatal("accepted")
			}
			if profile.calls != 1 || m.creates != 0 || len(profile.phases) != 0 {
				t.Fatal("effects before rejection")
			}
		})
	}
}
