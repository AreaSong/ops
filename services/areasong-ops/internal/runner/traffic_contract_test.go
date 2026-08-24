package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
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
		ServerID:      "server-demo",
		Metadata:      model.ObjectMetadata{Type: "service", Lifecycle: "active"},
		Adapter:       "/usr/local/libexec/areasong-ops/adapters/compose-service.sh",
		TrafficPolicy: testTrafficPolicy(),
		Actions: map[string]model.ActionDefinition{
			"inspect": {
				Name: "inspect", DisplayName: "Inspect", Enabled: true,
				Steps: []string{"inspect"}, TargetMode: "none",
			},
		},
	}
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

func TestRemoteWorkerRejectsTrafficPolicyContractMismatch(t *testing.T) {
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
			got := receiveCompletion(t, completion)
			if got.FailureCode != "dispatch_contract_invalid" || !got.Retryable {
				t.Fatalf("completion=%+v", got)
			}
			if !strings.Contains(got.Error, test.wantError) {
				t.Fatalf("error=%q, want %q", got.Error, test.wantError)
			}
		})
	}
}

func TestRemoteWorkerExecutesWhenTrafficPolicyContractMatches(t *testing.T) {
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
	got := receiveCompletion(t, completion)
	if got.State != model.TaskSucceeded || got.FailureCode != "" {
		t.Fatalf("completion=%+v", got)
	}
	if len(executor.inputs()) != 1 {
		t.Fatalf("executor calls=%d, want 1", len(executor.inputs()))
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
