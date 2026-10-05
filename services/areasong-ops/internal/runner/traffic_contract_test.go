package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func testTrafficPolicy() *model.TrafficPolicy {
	return &model.TrafficPolicy{
		AdapterPath:      model.TrafficAdapterPath,
		SiteFile:         "/etc/nginx/sites-enabled/demo.conf",
		IncludeFile:      "/etc/nginx/snippets/areasong-ops/demo-traffic.conf",
		Hostname:         "demo.example.com",
		MaintenanceFile:  "/etc/nginx/snippets/areasong-ops/demo-maintenance.conf",
		Marker:           "include /etc/nginx/snippets/areasong-ops/demo-traffic.conf;",
		DrainTimeoutSecs: 30,
	}
}

func testTrafficService() model.ServiceDefinition {
	return model.ServiceDefinition{
		Name:          "demo",
		ObjectID:      "service:demo",
		TenantID:      "default",
		ServerID:      "server-demo",
		Metadata:      model.ObjectMetadata{Type: "service", Lifecycle: "active"},
		Adapter:       "/usr/local/libexec/areasong-ops/adapters/compose-service.sh",
		TrafficPolicy: testTrafficPolicy(),
		AlertPolicy: model.AlertPolicyDefinition{
			Matchers:          map[string]string{"service": "demo"},
			BlockingAlerts:    []string{"AppHttpProbeFailed"},
			MaintenanceAlerts: []string{"AppHttpProbeFailed"},
		},
		Actions: map[string]model.ActionDefinition{
			"inspect": {
				Name: "inspect", DisplayName: "Inspect", Enabled: true,
				Steps: []string{"inspect"}, TargetMode: "none",
			},
		},
	}
}

func TestLifecycleFailureRestoresMaintenanceBarrier(t *testing.T) {
	// B-P 关闭普通计划主链；此项仅保留既有流量补偿选择的合成单元断言。
	for _, test := range []struct{ name, action, phase string }{{"stop drain failure", "stop", "drain"}, {"start final health failure", "start", "verify"}} {
		t.Run(test.name, func(t *testing.T) {
			executor := &lifecycleFaultExecutor{}
			engine, database := testEngine(t, executor)
			service := testTrafficService()
			result, attempted, err := engine.protectLifecycleFailure(model.Task{Action: test.action}, service, test.phase, t.TempDir())
			if err != nil || !attempted || !result.OK {
				t.Fatal("流量屏障选择退化", err)
			}
			calls := executor.inputs()
			if len(calls) != 1 || calls[0].Action != "enter-maintenance" || calls[0].Phase != "enter-maintenance" || calls[0].AdapterKind != adapterKindTraffic {
				t.Fatalf("calls=%+v", calls)
			}
			if _, found, err := database.GetServiceState(context.Background(), service.Name); err != nil || found {
				t.Fatal("失败单元写入desired state", err)
			}
			tasks, err := database.ListTasks(context.Background(), 200, 0)
			if err != nil || len(tasks) != 0 {
				t.Fatal("合成单元启动了普通任务", err)
			}
		})
	}
}

type lifecycleFaultExecutor struct {
	mu         sync.Mutex
	calls      []ExecuteInput
	failAction string
	failPhase  string
	failKind   string
	failMatch  int
	matches    int
}

func (executor *lifecycleFaultExecutor) Execute(_ context.Context, input ExecuteInput) (model.AdapterResult, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	executor.calls = append(executor.calls, input)
	if input.Action == executor.failAction && input.Phase == executor.failPhase && input.AdapterKind == executor.failKind {
		executor.matches++
		if executor.matches == executor.failMatch {
			return model.AdapterResult{}, errors.New("controlled lifecycle failure")
		}
	}
	data := map[string]any{
		"currentVersion": "1.0.0", "currentImage": "demo:v1.0.0@sha256:test",
		"currentImageId": "sha256:image", "runtimeIdentityHash": "sha256:runtime",
	}
	if input.AdapterKind == adapterKindTraffic {
		data["trafficState"] = "maintenance"
		data["includeDigest"] = "sha256:include"
		data["hostname"] = "demo.example.com"
		data["drainTimeoutSeconds"] = 30
	}
	return model.AdapterResult{SchemaVersion: 2, Action: input.Action, Phase: input.Phase, OK: true, Summary: input.Phase + " ok", Data: data}, nil
}

func (executor *lifecycleFaultExecutor) inputs() []ExecuteInput {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return append([]ExecuteInput(nil), executor.calls...)
}

func TestNewTaskDispatchCarriesTrafficPolicyDigest(t *testing.T) {
	service := testTrafficService()
	digest := service.PolicyDigest()

	t.Run("task field wins", func(t *testing.T) {
		task := model.Task{
			ID: "task-1", Service: service.Name, TrafficPolicyDigest: digest,
			Snapshot: map[string]any{"trafficPolicyDigest": "sha256:stale"},
		}
		dispatch := model.NewTaskDispatch(task)
		if dispatch.TrafficPolicyDigest != digest {
			t.Fatalf("dispatch digest=%q, want %q", dispatch.TrafficPolicyDigest, digest)
		}
	})

	t.Run("snapshot backfills legacy task", func(t *testing.T) {
		task := model.Task{
			ID: "task-2", Service: service.Name,
			Snapshot: map[string]any{"trafficPolicyDigest": digest},
		}
		dispatch := model.NewTaskDispatch(task)
		if dispatch.TrafficPolicyDigest != digest {
			t.Fatalf("dispatch digest=%q, want %q", dispatch.TrafficPolicyDigest, digest)
		}
	})
}

func TestBPRemoteWorkerDoesNotRewriteMismatchedContracts(t *testing.T) {
	service := testTrafficService()
	digest := service.PolicyDigest()
	cases := []struct {
		name      string
		dispatch  string
		snapshot  string
		wantError string
	}{
		{name: "dispatch drift", dispatch: "sha256:other", snapshot: digest, wantError: "流量策略摘要"},
		{name: "snapshot missing", dispatch: digest, snapshot: "", wantError: "流量策略摘要"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			completion := make(chan model.AssignmentCompletionRequest, 1)
			server := newWorkerContractServer(t, completion)
			defer server.Close()
			worker := &RemoteWorker{
				RunnerID: "runner-demo", Endpoint: server.URL, Client: server.Client(),
				Catalog:  &config.Catalog{Services: map[string]model.ServiceDefinition{"demo": service}},
				Executor: &contractTestExecutor{}, StateRoot: t.TempDir(), Lease: time.Hour,
			}
			task := model.Task{
				ID: "task-mismatch", Service: "demo", Action: "inspect",
				TrafficPolicyDigest: test.dispatch,
				Snapshot:            map[string]any{},
			}
			if test.snapshot != "" {
				task.Snapshot["trafficPolicyDigest"] = test.snapshot
			}
			worker.execute(context.Background(), model.AssignmentClaimResponse{
				Task: model.NewTaskDispatch(task),
				Assignment: model.TaskAssignment{
					ServerID: "server-demo", Generation: 1, ClaimToken: "claim",
				},
			})
			select {
			case got := <-completion:
				t.Fatalf("B-P改写远端旧任务: %+v", got)
			default:
			}
			if len(worker.Executor.(*contractTestExecutor).inputs()) != 0 {
				t.Fatal("拒绝后远端执行")
			}

		})
	}
}

func TestBPRemoteWorkerRejectsEvenMatchingContracts(t *testing.T) {
	service := testTrafficService()
	digest := service.PolicyDigest()
	completion := make(chan model.AssignmentCompletionRequest, 1)
	server := newWorkerContractServer(t, completion)
	defer server.Close()
	executor := &contractTestExecutor{}
	worker := &RemoteWorker{
		RunnerID: "runner-demo", Endpoint: server.URL, Client: server.Client(),
		Catalog:  &config.Catalog{Services: map[string]model.ServiceDefinition{"demo": service}},
		Executor: executor, StateRoot: t.TempDir(), Lease: time.Hour,
	}
	task := model.Task{
		ID: "task-match", Service: "demo", Action: "inspect", TrafficPolicyDigest: digest,
		Snapshot: map[string]any{"trafficPolicyDigest": digest},
		Stages:   []model.TaskStage{{Name: "inspect"}},
	}
	worker.execute(context.Background(), model.AssignmentClaimResponse{
		Task: model.NewTaskDispatch(task),
		Assignment: model.TaskAssignment{
			ServerID: "server-demo", Generation: 1, ClaimToken: "claim",
			ExecutionDeadlineAt: time.Now().Add(time.Minute),
		},
	})
	select {
	case got := <-completion:
		t.Fatalf("B-P补报远端终态: %+v", got)
	default:
	}
	if len(executor.inputs()) != 0 {
		t.Fatal("匹配合同也不得绕过B-P")
	}
}

func TestCompositeWebsiteLifecycleUsesTrafficBarrierAndApplicationPhases(t *testing.T) {
	service := testTrafficService()
	for _, actionName := range []string{"stop", "start"} {
		action, ok := lifecycleAction(service, actionName)
		if !ok {
			t.Fatalf("%s lifecycle action not exposed", actionName)
		}
		executor := &contractTestExecutor{}
		for _, phase := range action.Steps {
			if _, err := executeAdapterPhase(context.Background(), executor, service, actionName, phase, t.TempDir(), "", ""); err != nil {
				t.Fatalf("%s phase %s: %v", actionName, phase, err)
			}
		}
		calls := executor.inputs()
		if actionName == "stop" {
			want := []ExecuteInput{
				{Action: "drain", Phase: "preflight", AdapterKind: adapterKindTraffic},
				{Action: "stop", Phase: "preflight", AdapterKind: adapterKindService},
				{Action: "drain", Phase: "drain", AdapterKind: adapterKindTraffic},
				{Action: "enter-maintenance", Phase: "enter-maintenance", AdapterKind: adapterKindTraffic},
				{Action: "stop", Phase: "stop", AdapterKind: adapterKindService},
				{Action: "enter-maintenance", Phase: "health", AdapterKind: adapterKindTraffic},
				{Action: "stop", Phase: "health", AdapterKind: adapterKindService},
			}
			assertLifecycleCalls(t, calls, want)
		} else {
			want := []ExecuteInput{
				{Action: "enter-maintenance", Phase: "preflight", AdapterKind: adapterKindTraffic},
				{Action: "start", Phase: "preflight", AdapterKind: adapterKindService},
				{Action: "enter-maintenance", Phase: "enter-maintenance", AdapterKind: adapterKindTraffic},
				{Action: "start", Phase: "start", AdapterKind: adapterKindService},
				{Action: "start", Phase: "health", AdapterKind: adapterKindService},
				{Action: "resume-traffic", Phase: "resume-traffic", AdapterKind: adapterKindTraffic},
				{Action: "resume-traffic", Phase: "verify", AdapterKind: adapterKindTraffic},
				{Action: "inspect", Phase: "inspect", AdapterKind: adapterKindService},
			}
			assertLifecycleCalls(t, calls, want)
		}
	}
}

func TestLifecycleObservationOverrideIsEngineScoped(t *testing.T) {
	service := model.ServiceDefinition{
		Name: "demo", Metadata: model.ObjectMetadata{Type: "service", Lifecycle: "active"},
	}
	production := &Engine{lifecycleObservationSeconds: -1}
	productionAction, ok := production.lifecycleAction(service, "stop")
	if !ok || productionAction.ObservationSeconds != 300 {
		t.Fatalf("production lifecycle observation=%d ok=%v, want 300", productionAction.ObservationSeconds, ok)
	}
	acceptance := &Engine{lifecycleObservationSeconds: 1}
	acceptanceAction, ok := acceptance.lifecycleAction(service, "stop")
	if !ok || acceptanceAction.ObservationSeconds != 1 {
		t.Fatalf("acceptance lifecycle observation=%d ok=%v, want 1", acceptanceAction.ObservationSeconds, ok)
	}
}

func assertLifecycleCalls(t *testing.T, got, want []ExecuteInput) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("calls=%+v, want %d calls", got, len(want))
	}
	for index := range want {
		if got[index].Action != want[index].Action || got[index].Phase != want[index].Phase || got[index].AdapterKind != want[index].AdapterKind {
			t.Fatalf("call[%d]=%+v, want %+v", index, got[index], want[index])
		}
	}
}

type contractTestExecutor struct {
	mu    sync.Mutex
	calls []ExecuteInput
}

func (executor *contractTestExecutor) Execute(_ context.Context, input ExecuteInput) (model.AdapterResult, error) {
	executor.mu.Lock()
	executor.calls = append(executor.calls, input)
	executor.mu.Unlock()
	return model.AdapterResult{SchemaVersion: 2, Action: input.Action, Phase: input.Phase, OK: true, Summary: "ok"}, nil
}

func (executor *contractTestExecutor) inputs() []ExecuteInput {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return append([]ExecuteInput(nil), executor.calls...)
}

func newWorkerContractServer(t *testing.T, completion chan<- model.AssignmentCompletionRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if path.Base(request.URL.Path) == "complete" {
			var input model.AssignmentCompletionRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Errorf("decode completion: %v", err)
			} else {
				completion <- input
			}
		}
		response.WriteHeader(http.StatusNoContent)
	}))
}

func receiveCompletion(t *testing.T, completion <-chan model.AssignmentCompletionRequest) model.AssignmentCompletionRequest {
	t.Helper()
	select {
	case input := <-completion:
		return input
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker completion")
		return model.AssignmentCompletionRequest{}
	}
}
