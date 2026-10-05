package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type sub2apiMemorySilence struct{ fakeAlertmanager }

func (*sub2apiMemorySilence) sub2apiEndpoint() string { return "http://synthetic.invalid" }

func (m *sub2apiMemorySilence) SilenceExpired(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, expired := range m.expired {
		if expired == id {
			return true, nil
		}
	}
	return false, nil
}

func sub2apiEngine(t *testing.T) (*Engine, *sub2apiFixture) {
	t.Helper()
	e, _ := preparationEngine(t)
	root, err := filepath.EvalSymlinks(e.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	e.stateRoot = root
	x := newSub2APIFixture(t)
	r := x.b.m.Resources["operation-root"]
	r.Selector = filepath.Join(root, "operations")
	x.b.m.Resources["operation-root"] = r
	r = x.b.m.Resources["inspection-root"]
	r.Selector = root
	x.b.m.Resources["inspection-root"] = r
	x.saveManifest(t)
	a := e.catalog.Services["demo"].Actions["update"]
	a.Steps = append([]string(nil), sub2apiPhases...)
	a.ObservationSeconds = 0
	a.PhaseSemantics = map[string]model.PhaseSemantics{
		"preflight": {Effect: "observe", FailurePolicy: "fail"},
		"backup":    {Effect: "artifact_write", ProducesRecoveryPoint: true, FailurePolicy: "fail"},
		"migration": {Effect: "observe", RequiresRecoveryPoint: true, FailurePolicy: "fail"},
		"apply":     {Effect: "runtime_mutation", RequiresRecoveryPoint: true, FailurePolicy: "rollback", RecoveryPhase: "rollback"},
		"health":    {Effect: "observe", FailurePolicy: "rollback", RecoveryPhase: "rollback"},
		"smoke":     {Effect: "observe", FailurePolicy: "rollback", RecoveryPhase: "rollback"},
		"identity":  {Effect: "observe", FailurePolicy: "rollback", RecoveryPhase: "rollback"},
	}
	x.action = a
	x.service.Actions["update"] = a
	x.service.RecoveryPointPolicy = &model.RecoveryPointPolicy{RequiredArtifactRoles: []string{"postgres-sub2api", "redis", "volume-sub2api-data", "configs", "runtime-snapshot"}, RecoverableSeconds: 3600}
	e.executor = x.p
	e.catalog.Services["sub2api"] = x.service
	if e.catalog.AutomaticTasks == nil {
		e.catalog.AutomaticTasks = map[string]model.ServiceDefinition{}
	}
	e.catalog.AutomaticTasks["backup-coordination"] = model.ServiceDefinition{Name: "backup-coordination", ObjectID: "coordination:backup", TenantID: "team", ServerID: "local"}
	e.backupRoot = x.b.m.Resources["backup-root"].Selector
	e.alertmanager = &sub2apiMemorySilence{}
	// 仅本测试临时 SQLite 中标明合成发现；从不更新真实 prepared/catalog。
	db := brDB(t, e)
	if _, err = db.Exec(`UPDATE tasks SET service='sub2api' WHERE service='demo' AND action='check'`); err != nil {
		t.Fatal(err)
	}
	x.b.completeHook = func(c *sub2apiCompletion) {}

	return e, x
}

func TestSub2APIBPBRCreationApprovalExecutionClosure(t *testing.T) {
	e, x := sub2apiEngine(t)
	p := brApproved(t, e, model.PreviewRequest{Service: "sub2api", Action: "update", Target: x.b.m.Target, IdempotencyKey: mustUUID(t)})
	approved := p.Digest
	task, _ := brExecute(t, e, p)
	e.Wait()
	brState(t, e, task, model.PlanCompleted, model.WorkClosed)
	p, err := e.store.GetReleasePlan(context.Background(), p.ID)
	if err != nil || p.Digest != approved {
		t.Fatal("approved digest changed", err)
	}
	if len(x.b.calls) != 10 {
		t.Fatalf("expected create, pre-exec, 7 stages, close; got %d", len(x.b.calls))
	}
	lease := e.releaseLease(task.ID)
	// owner 不进入私有上下文、HTTP、task-contract、阶段回执或事件日志。
	raw, _ := json.Marshal(x.input("identity"))
	if strings.Contains(string(raw), lease.owner) {
		t.Fatal("serialized owner")
	}
	response := postPreparation(t, e, actorHash(), "/v1/plans/"+p.ID+"/close", model.ClosePlanRequest{IdempotencyKey: mustUUID(t)})
	if strings.Contains(response.Body.String(), lease.owner) {
		t.Fatal("http owner")
	}
	for _, root := range []string{x.b.journal, filepath.Join(e.stateRoot, "operations")} {
		err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(raw), lease.owner) {
				t.Fatal("disk owner", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := brDB(t, e).Query(`SELECT data_json,message FROM events WHERE task_id=?`, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var data, message string
		if err = rows.Scan(&data, &message); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(data+message, lease.owner) {
			t.Fatal("event owner")
		}
	}
}

func TestSub2APIBRPartialFailureRollbackAndUncertain(t *testing.T) {
	for _, mode := range []string{"controlled", "pair", "target", "unknown migration", "late writer", "save failure"} {
		t.Run(mode, func(t *testing.T) {
			e, x := sub2apiEngine(t)
			original := x.b.completeHook
			x.b.fail = "apply"
			x.b.partial = mode
			if mode == "unknown migration" || mode == "late writer" || mode == "save failure" {
				x.b.fail = ""
				x.b.partial = ""
				x.b.completeHook = func(c *sub2apiCompletion) {
					original(c)
					if c.Request.Phase != "apply" {
						return
					}
					if mode == "unknown migration" {
						c.After.Migration = model.WorkDigest("unknown")
					}
					if mode == "late writer" {
						c.Facts.Successors.Status = "running"
					}
					if mode == "save failure" {
						x.b.saveHook = func([]byte) ([]byte, error) { return nil, errSub2API }
					}
				}
			}
			p := brApproved(t, e, model.PreviewRequest{Service: "sub2api", Action: "update", Target: x.b.m.Target, IdempotencyKey: mustUUID(t)})
			task, _ := brExecute(t, e, p)
			e.Wait()
			if mode == "controlled" || mode == "pair" || mode == "target" {
				brState(t, e, task, model.PlanNeedsAttention, model.WorkClosed)
				actual, _ := e.store.GetTask(context.Background(), task.ID)
				if actual.State != model.TaskRolledBack {
					t.Fatal(actual.State, actual.Error)
				}
			} else {
				brState(t, e, task, model.PlanNeedsAttention, model.WorkUncertain)
				if x.b.calls[len(x.b.calls)-1].Phase != "apply" {
					t.Fatal("continued after uncertainty")
				}
			}
		})
	}
}

func TestSub2APICanceledLateWriterCannotSettle(t *testing.T) {
	x := newSub2APIFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	late := make(chan struct{})
	done := make(chan struct{})
	path := filepath.Join(x.root, "synthetic-late-writer")
	x.b.completeHook = func(c *sub2apiCompletion) {
		go func() { defer close(done); <-late; _ = os.WriteFile(path, []byte("synthetic delayed write"), 0600) }()
		cancel()
	}
	c := x.p.executeReleaseCall(ctx, x.input("preflight"))
	if c.Settled != nil {
		t.Fatal("canceled call settled")
	}
	close(late)
	<-done
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if c = x.p.executeReleaseCall(context.Background(), x.input("preflight")); c.Settled != nil || len(x.b.calls) != 1 {
		t.Fatal("late completion advanced")
	}
}

func TestSub2APIBPBRWithNginxInspection(t *testing.T) {
	e, x := sub2apiEngine(t)
	policy := &model.TrafficPolicy{AdapterPath: model.TrafficAdapterPath, Hostname: "synthetic.invalid", Marker: "synthetic", DrainTimeoutSecs: 10,
		SiteFile: filepath.Join(x.root, "site.conf"), IncludeFile: filepath.Join(x.root, "include.conf"), MaintenanceFile: filepath.Join(x.root, "maintenance.conf")}
	x.b.m.Nginx = policy
	x.service.TrafficPolicy = policy
	x.b.m.NginxFiles = map[string]string{}
	for _, path := range []string{policy.SiteFile, policy.IncludeFile, policy.MaintenanceFile} {
		b := []byte("synthetic nginx config")
		writeSub2APIFile(t, path, b)
		x.b.m.NginxFiles[path] = model.WorkDigest(string(b))
	}
	r := x.b.m.Resources["nginx"]
	r.Selector = sub2apiDigest(policy)
	x.b.m.Resources["nginx"] = r
	x.saveManifest(t)
	e.catalog.Services["sub2api"] = x.service
	if e.catalog.AutomaticTasks == nil {
		e.catalog.AutomaticTasks = map[string]model.ServiceDefinition{}
	}
	e.catalog.AutomaticTasks["backup-coordination"] = model.ServiceDefinition{Name: "backup-coordination", ObjectID: "coordination:backup", TenantID: "team", ServerID: "local"}
	p := brApproved(t, e, model.PreviewRequest{Service: "sub2api", Action: "update", Target: x.b.m.Target, IdempotencyKey: mustUUID(t)})
	task, _ := brExecute(t, e, p)
	e.Wait()
	brState(t, e, task, model.PlanCompleted, model.WorkClosed)
	traffic := 0
	for _, c := range x.b.calls {
		if c.Kind == adapterKindTraffic {
			traffic++
			if c.Action != "inspect" {
				t.Fatal("traffic mutation")
			}
		}
	}
	if traffic != 3 {
		t.Fatal("missing traffic checks", traffic)
	}
}

type wrongSub2APISilence struct{ sub2apiMemorySilence }

func (*wrongSub2APISilence) sub2apiEndpoint() string { return "http://wrong.invalid" }

func TestSub2APIEngineBindsActualAlertmanager(t *testing.T) {
	for _, mode := range []string{"wrong identity", "no identity", "production client"} {
		t.Run(mode, func(t *testing.T) {
			e, x := sub2apiEngine(t)
			switch mode {
			case "wrong identity":
				e.alertmanager = &wrongSub2APISilence{}
			case "no identity":
				e.alertmanager = &fakeAlertmanager{}
			case "production client":
				c, err := NewAlertmanagerClient("http://127.0.0.1:9093")
				if err != nil {
					t.Fatal(err)
				}
				c.client.Transport = sub2apiDenyTransport{t: t}
				e.alertmanager = c
			}
			_, err := e.createManualReleasePlan(context.Background(), actorHash(), model.PreviewRequest{Service: "sub2api", Action: "update", Target: x.b.m.Target, IdempotencyKey: mustUUID(t)})
			if err == nil || len(x.b.calls) != 0 {
				t.Fatal("unbound client accepted or called")
			}
		})
	}
}

func TestSub2APIEngineBindsActualDirectories(t *testing.T) {
	for _, role := range []string{"operation-root", "inspection-root", "backup-root"} {
		t.Run(role, func(t *testing.T) {
			e, x := sub2apiEngine(t)
			r := x.b.m.Resources[role]
			r.Selector = filepath.Join(x.root, "sub2api", "wrong", role)
			x.b.m.Resources[role] = r
			x.saveManifest(t)
			e.catalog.Services["sub2api"] = x.service
			if e.catalog.AutomaticTasks == nil {
				e.catalog.AutomaticTasks = map[string]model.ServiceDefinition{}
			}
			e.catalog.AutomaticTasks["backup-coordination"] = model.ServiceDefinition{Name: "backup-coordination", ObjectID: "coordination:backup", TenantID: "team", ServerID: "local"}
			_, err := e.createManualReleasePlan(context.Background(), actorHash(), model.PreviewRequest{Service: "sub2api", Action: "update", Target: x.b.m.Target, IdempotencyKey: mustUUID(t)})
			if err == nil || len(x.b.calls) != 0 {
				t.Fatal("unbound directory accepted")
			}
		})
	}
}

type sub2apiDenyTransport struct{ t *testing.T }

func (d sub2apiDenyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	d.t.Error("unexpected external HTTP access")
	return nil, errors.New("synthetic network sentinel")
}
