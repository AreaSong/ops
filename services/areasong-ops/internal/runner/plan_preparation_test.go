package runner

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

type syntheticInspection struct {
	mu           sync.Mutex
	scope        model.ReleaseScopeDefinition
	calls        int
	entered      chan struct{}
	release      chan struct{}
	uncertain    bool
	failedResult bool
	failureErr   error
	after        func(ExecuteInput)
}

func (s *syntheticInspection) Execute(context.Context, ExecuteInput) (model.AdapterResult, error) {
	return model.AdapterResult{}, errors.New("B-P 不允许调用普通执行器")
}
func (s *syntheticInspection) resolvePlanInspectionScope(context.Context, model.ServiceDefinition, model.ActionDefinition, releaseScopeInput) (model.ReleaseScopeDefinition, error) {
	return s.scope, nil
}
func (s *syntheticInspection) executePlanInspection(ctx context.Context, input ExecuteInput) inspectionCall {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.entered != nil {
		close(s.entered)
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return inspectionCall{Err: ctx.Err()}
		}
	}
	if s.after != nil {
		s.after(input)
	}
	if s.uncertain {
		return inspectionCall{Err: errors.New("合成迟到结果")}
	}
	done := make(chan struct{})
	close(done)
	return inspectionCall{Result: model.AdapterResult{OK: !s.failedResult, Summary: "合成检查", Data: map[string]any{"currentVersion": "1.0.0", "currentImage": "synthetic:image", "runtimeIdentityHash": "synthetic:identity"}},
		Err: s.failureErr, Settled: done, EvidenceDigest: model.WorkDigest("synthetic call " + input.AdapterKind)}
}

func preparationEngine(t *testing.T) (*Engine, *syntheticInspection) {
	t.Helper()
	profile := &syntheticInspection{}
	engine, db := testEngine(t, profile)
	ctx := context.Background()
	if err := db.EnsureAccessDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	policy := config.AccessPolicy{Enforced: true, DefaultTenant: "default",
		Tenants: map[string]model.Tenant{"default": {ID: "default", DisplayName: "Default", Status: "active", CreatedBy: "bootstrap"},
			"team":  {ID: "team", DisplayName: "Team", Status: "active", CreatedBy: actorHash()},
			"other": {ID: "other", DisplayName: "Other", Status: "active", CreatedBy: actorHash()}},
		Roles: map[string]model.Role{"platform-admin": {ID: "platform-admin", Permissions: []model.Permission{"*"}}},
		Principals: map[string]config.AccessPrincipal{
			actorHash():             {Subject: actorHash(), TenantID: "default", Status: "active", Roles: []string{"platform-admin"}},
			strings.Repeat("b", 64): {Subject: strings.Repeat("b", 64), TenantID: "other", Status: "active", Roles: []string{"platform-admin"}}}}
	for _, tenant := range policy.Tenants {
		if err := db.UpsertTenant(ctx, tenant); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(policy)
	if _, err := db.SaveAccessPolicySnapshot(ctx, model.AccessPolicySnapshot{ActorHash: actorHash(), PolicyJSON: string(raw), Digest: "sha256:" + model.WorkDigest(string(raw))}, 0); err != nil {
		t.Fatal(err)
	}
	engine.catalog.Access = &policy
	engine.catalog.SchemaVersion = 4
	implementation := filepath.Join(engine.stateRoot, "synthetic-profile.txt")
	if err := os.WriteFile(implementation, []byte("synthetic profile; never executable"), 0600); err != nil {
		t.Fatal(err)
	}
	profile.scope = model.ReleaseScopeDefinition{Version: 1, Mode: "local", ProfileID: "synthetic-test-only",
		Resources:             []model.ReleaseScopeResource{{Kind: "application", Selector: "synthetic/demo", ObjectID: "service:demo"}, {Kind: "backup", Selector: "synthetic/other", ObjectID: "service:other"}},
		ImplementationDigests: map[string]string{implementation: model.WorkDigest("synthetic profile; never executable")}}
	service := engine.catalog.Services["demo"]
	service.TenantID = "team"
	service.ServerID = "local"
	service.Metadata.Type = "service"
	service.ReleaseScope = &profile.scope
	engine.catalog.Services["demo"] = service
	other := service
	other.Name = "other"
	other.ObjectID = "service:other"
	other.TenantID = "other"
	other.ReleaseScope = nil
	engine.catalog.Services["other"] = other
	seedHistoricalDiscovery(t, engine)
	return engine, profile
}

func seedHistoricalDiscovery(t *testing.T, engine *Engine) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(engine.stateRoot, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id := mustUUID(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.Exec(`INSERT INTO tasks(id,idempotency_key,request_hash,actor_hash,service,action,target,risk,state,preview_id,snapshot_json,stages_json,created_at,finished_at) VALUES(?,?,?,?,'demo','check','','read_only','succeeded','','{}','[]',?,?)`, id, id, store.HashConfirmation(id), actorHash(), now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.store.AppendEvent(context.Background(), model.Event{TaskID: id, Level: "info", Phase: "discover", Data: map[string]any{"currentVersion": "1.0.0", "latestTag": "v1.1.0", "prepared": true}})
	if err != nil {
		t.Fatal(err)
	}
}

func bpRequest(t *testing.T) model.PreviewRequest {
	return model.PreviewRequest{Service: "demo", Action: "update", Target: "v1.1.0", IdempotencyKey: mustUUID(t)}
}

func postPreparation(t *testing.T, engine *Engine, actor, path string, input any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set(actorHeader, actor)
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewServer(engine, engine.store).ServeHTTP(response, req)
	return response
}

func TestPreparationHTTPBindsTargetsApprovesButNeverExecutes(t *testing.T) {
	engine, profile := preparationEngine(t)
	request := bpRequest(t)
	response := postPreparation(t, engine, actorHash(), "/v1/plans", request)
	if response.Code != http.StatusCreated {
		t.Fatal(response.Code, response.Body.String())
	}
	var plan model.ReleasePlan
	if err := json.Unmarshal(response.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.ApprovalSummary.SchemaVersion != 2 || len(plan.Digest) != 64 || plan.ApprovalSummary.Lifecycle == nil {
		t.Fatal("v2合同缺失")
	}
	binding := plan.ApprovalSummary.Lifecycle
	if len(binding.Targets) != 3 || binding.CreatorTenantID != "default" || len(binding.TargetObjects) != 2 {
		t.Fatalf("目标不完整: %+v", binding)
	}
	beforeCalls := profile.calls
	replay := postPreparation(t, engine, actorHash(), "/v1/plans", request)
	if replay.Code != http.StatusCreated || profile.calls != beforeCalls {
		t.Fatal("创建重放调用inspect")
	}
	approval := model.ApprovePlanRequest{Digest: plan.Digest, Confirmation: plan.ConfirmationPhrase}
	if r := postPreparation(t, engine, actorHash(), "/v1/plans/"+plan.ID+"/approve", approval); r.Code < 400 {
		t.Fatal("创建者自批")
	}
	if r := postPreparation(t, engine, strings.Repeat("b", 64), "/v1/plans/"+plan.ID+"/approve", approval); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	for _, action := range []string{"execute", "close"} {
		r := postPreparation(t, engine, actorHash(), "/v1/plans/"+plan.ID+"/"+action, map[string]string{"idempotencyKey": mustUUID(t)})
		if r.Code < 400 {
			t.Fatal(action + "绕过B-P门禁")
		}
	}
	if profile.calls != beforeCalls {
		t.Fatal("执行或收口有适配器调用")
	}
	manager := engine.alertmanager.(*fakeAlertmanager)
	if len(manager.created) != 0 || len(manager.expired) != 0 {
		t.Fatal("B-P调用静默")
	}
	if _, err := os.Stat(filepath.Join(engine.stateRoot, "operations")); !os.IsNotExist(err) {
		t.Fatal("B-P创建任务目录")
	}
	r, err := engine.store.GetPlanPreparation(context.Background(), binding.PreparationID)
	if err != nil || r.State != model.WorkClosed || r.ProducedPlanID != plan.ID {
		t.Fatal(err)
	}
}

func TestPreparationConcurrentRequestGetsOneInspection(t *testing.T) {
	engine, profile := preparationEngine(t)
	profile.entered = make(chan struct{})
	profile.release = make(chan struct{})
	request := bpRequest(t)
	done := make(chan error, 1)
	go func() {
		_, err := engine.createManualReleasePlan(context.Background(), actorHash(), request)
		done <- err
	}()
	select {
	case <-profile.entered:
	case err := <-done:
		t.Fatalf("检查前被拒绝: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("未到达检查屏障")
	}
	if _, err := engine.createManualReleasePlan(context.Background(), actorHash(), request); err == nil {
		t.Fatal("进行中请求取得第二权")
	}
	close(profile.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	profile.mu.Lock()
	calls := profile.calls
	profile.mu.Unlock()
	if calls != 1 {
		t.Fatal("重复检查", calls)
	}
}

func TestPreparationUnknownAndDriftStayBlocked(t *testing.T) {
	for _, scenario := range []string{"default_executor", "missing_object", "implementation_drift", "unknown_completion", "scope_changes"} {
		t.Run(scenario, func(t *testing.T) {
			engine, profile := preparationEngine(t)
			switch scenario {
			case "default_executor":
				engine.executor = CommandExecutor{}
			case "missing_object":
				delete(engine.catalog.Services, "other")
			case "implementation_drift":
				for path := range profile.scope.ImplementationDigests {
					if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "unknown_completion":
				profile.uncertain = true
			case "scope_changes":
				profile.after = func(ExecuteInput) {
					s := engine.catalog.Services["other"]
					s.ServerID = "changed"
					engine.catalog.Services["other"] = s
				}
			}
			request := bpRequest(t)
			if _, err := engine.createManualReleasePlan(context.Background(), actorHash(), request); err == nil {
				t.Fatal("未拒绝")
			}
			r, found, err := engine.store.GetPlanPreparationByRequest(context.Background(), request.IdempotencyKey)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "unknown_completion" || scenario == "scope_changes" {
				if !found || r.State != model.WorkUncertain {
					t.Fatal("不确定结果释放", r.State)
				}
			} else if found || profile.calls != 0 {
				t.Fatal("最早副作用未被拒绝")
			}
		})
	}
}

func TestPreparationFailedResultCannotProducePlan(t *testing.T) {
	engine, profile := preparationEngine(t)
	profile.failedResult = true
	request := bpRequest(t)
	if _, err := engine.createManualReleasePlan(context.Background(), actorHash(), request); err == nil {
		t.Fatal("OK=false生成计划")
	}
	r, found, err := engine.store.GetPlanPreparationByRequest(context.Background(), request.IdempotencyKey)
	if err != nil || !found || r.State != model.WorkClosed || r.ProducedPlanID != "" {
		t.Fatal("已收敛失败结果处理不正确", err)
	}
	if _, err = engine.store.GetReleasePlan(context.Background(), r.Request.PlanID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("失败检查留下计划")
	}
}

func TestPreparationRedactsFailedInspection(t *testing.T) {
	engine, profile := preparationEngine(t)
	profile.failureErr = errors.New("password=synthetic-secret failure")
	request := bpRequest(t)
	if _, err := engine.createManualReleasePlan(context.Background(), actorHash(), request); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatal("检查失败未脱敏")
	}
	record, _, err := engine.store.GetPlanPreparationByRequest(context.Background(), request.IdempotencyKey)
	if err != nil || record.State != model.WorkClosed || strings.Contains(record.ResultJSON, "synthetic-secret") {
		t.Fatal("持久化失败信息泄漏", err)
	}
}
