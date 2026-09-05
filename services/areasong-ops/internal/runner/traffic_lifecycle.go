package runner

import (
	"context"
	"errors"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func isTrafficLifecycleAction(action string) bool {
	switch action {
	case "enter-maintenance", "drain", "resume-traffic":
		return true
	default:
		return false
	}
}

// trafficActionForLifecyclePhase maps composite website actions onto the
// fixed traffic adapter contract. The public action remains start/stop so the
// plan and audit describe the user's intent.
func trafficActionForLifecyclePhase(action, phase string) (string, bool) {
	switch action {
	case "enter-maintenance", "drain", "resume-traffic":
		return action, true
	case "stop":
		switch phase {
		case "preflight", "drain":
			return "drain", true
		case "enter-maintenance", "health":
			return "enter-maintenance", true
		}
	case "start":
		switch phase {
		case "preflight", "enter-maintenance":
			return "enter-maintenance", true
		case "resume-traffic", "verify":
			return "resume-traffic", true
		}
	}
	return "", false
}

func lifecycleAppProbe(action, phase string) (string, string, bool) {
	if action == "start" || action == "stop" {
		if phase == "preflight" || phase == "health" {
			return action, phase, true
		}
	}
	if phase == "preflight" || phase == "health" || phase == "verify" {
		return "inspect", "inspect", true
	}
	return "", "", false
}

func (engine *Engine) desiredStateInputForTask(task model.Task) *store.DesiredStateInput {
	desired, ok := desiredStateForAction(task.Action)
	if !ok || task.State != model.TaskSucceeded {
		return nil
	}
	service, found := engine.catalog.Services[task.Service]
	if !found || (isTrafficLifecycleAction(task.Action) && service.TrafficPolicy == nil) {
		return nil
	}
	tenantID := service.TenantID
	if tenantID == "" {
		tenantID = "default"
	}
	return &store.DesiredStateInput{
		Service: service.Name, ObjectID: service.ObjectID, TenantID: tenantID,
		Desired: desired, Reason: "流量适配器执行完成", ActorHash: task.ActorHash,
		MaintenanceUntil: maintenanceUntilForTask(service, desired),
	}
}

func maintenanceUntilForTask(service model.ServiceDefinition, desired model.DesiredState) *time.Time {
	if desired != model.DesiredMaintenance && desired != model.DesiredDrained {
		return nil
	}
	seconds := 4 * 60 * 60
	if service.StatePolicy != nil && service.StatePolicy.MaintenanceTTLSeconds > 0 {
		seconds = service.StatePolicy.MaintenanceTTLSeconds
	}
	value := time.Now().UTC().Add(time.Duration(seconds) * time.Second)
	return &value
}

func (engine *Engine) executePhase(
	ctx context.Context,
	service model.ServiceDefinition,
	action, phase, operationDir, target, sourceDir string,
) (model.AdapterResult, error) {
	return executeAdapterPhase(ctx, engine.executor, service, action, phase, operationDir, target, sourceDir)
}

func executeAdapterPhase(
	ctx context.Context,
	executor Executor,
	service model.ServiceDefinition,
	action, phase, operationDir, target, sourceDir string,
) (model.AdapterResult, error) {
	trafficAction, hasTraffic := trafficActionForLifecyclePhase(action, phase)
	if service.TrafficPolicy == nil {
		hasTraffic = false
	}
	if hasTraffic {
		if service.TrafficPolicy == nil {
			return model.AdapterResult{}, errors.New("服务未声明受信流量策略")
		}
		trafficResult, err := executor.Execute(ctx, ExecuteInput{
			Service: service, Action: trafficAction, Phase: phase,
			OperationDir: operationDir, Target: target, AdapterKind: adapterKindTraffic,
		})
		if err != nil {
			return model.AdapterResult{}, err
		}
		appAction, appPhase, needsAppProbe := lifecycleAppProbe(action, phase)
		if !needsAppProbe {
			return trafficResult, nil
		}
		// Traffic changes must also prove that the application remains healthy.
		// The application adapter is invoked through its fixed inspect contract;
		// no lifecycle request can select an executable or pass arbitrary args.
		appResult, appErr := executor.Execute(ctx, ExecuteInput{
			Service: service, Action: appAction, Phase: appPhase,
			OperationDir: operationDir, AdapterKind: adapterKindService,
		})
		if appErr != nil {
			return model.AdapterResult{}, appErr
		}
		merged := cloneAnyMap(trafficResult.Data)
		if merged == nil {
			merged = make(map[string]any)
		}
		merged["application"] = appResult.Data
		for key, value := range appResult.Data {
			if _, exists := merged[key]; !exists {
				merged[key] = value
			}
		}
		trafficResult.Data = merged
		trafficResult.Summary = trafficResult.Summary + "; 应用检查通过"
		return trafficResult, nil
	}
	return executor.Execute(ctx, ExecuteInput{
		Service: service, Action: action, Phase: phase,
		OperationDir: operationDir, Target: target, SourceDir: sourceDir,
		AdapterKind: adapterKindService,
	})
}

// protectLifecycleFailure restores the public maintenance barrier after a
// lifecycle phase may have exposed or left traffic in an uncertain state. It
// is a bounded compensating action, not an automatic retry of the application
// mutation that failed.
func (engine *Engine) protectLifecycleFailure(
	task model.Task,
	service model.ServiceDefinition,
	failedPhase, operationDir string,
) (model.AdapterResult, bool, error) {
	if service.TrafficPolicy == nil {
		return model.AdapterResult{}, false, nil
	}
	needsBarrier := task.Action == "stop" &&
		(failedPhase == "drain" || failedPhase == "enter-maintenance" ||
			failedPhase == "stop" || failedPhase == "health")
	needsBarrier = needsBarrier || task.Action == "start" &&
		(failedPhase == "resume-traffic" || failedPhase == "verify")
	if !needsBarrier {
		return model.AdapterResult{}, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := engine.executePhase(ctx, service, "enter-maintenance", "enter-maintenance", operationDir, "", "")
	return result, true, err
}
