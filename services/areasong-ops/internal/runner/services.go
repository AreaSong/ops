package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func (engine *Engine) Services(ctx context.Context) []model.ServiceView {
	return engine.serviceViews(ctx, engine.catalog.ServiceNames())
}

func (engine *Engine) ServicesForActor(
	ctx context.Context,
	actor string,
) ([]model.ServiceView, error) {
	names, err := engine.authorizedObjectNames(ctx, actor, engine.catalog.ServiceNames())
	if err != nil {
		return nil, err
	}
	return engine.serviceViews(ctx, names), nil
}

func (engine *Engine) serviceViews(ctx context.Context, names []string) []model.ServiceView {
	views := make([]model.ServiceView, len(names))
	var wait sync.WaitGroup
	for index, name := range names {
		index, service := index, engine.catalog.Services[name]
		wait.Add(1)
		go func() {
			defer wait.Done()
			view := model.ServiceView{
				Name: service.Name, ObjectID: service.ObjectID, DisplayName: service.DisplayName,
				Metadata: service.Metadata, Description: service.Description,
				ManagedCompose: service.Runtime != nil, Actions: engine.exposedActions(service),
			}
			view.TenantID, view.ServerID = service.TenantID, service.ServerID
			status, err := engine.inspect(ctx, service)
			if err != nil {
				view.StatusError = redactText(err.Error())
			} else {
				view.Status = status
			}
			if state, found, stateErr := engine.store.GetServiceState(ctx, service.Name); stateErr == nil && found {
				view.State = &state
				view.Drift = state.Drift
			}
			if active, found, err := engine.store.ActiveTask(ctx, service.Name); err == nil && found {
				view.ActiveTaskID = active.ID
			}
			if discovery, found, err := engine.store.LatestSuccessfulDiscovery(ctx, service.Name); err == nil && found {
				view.ReleaseDiscovery = discovery
			}
			if source, found, err := engine.store.LatestSucceededUpdate(ctx, service.Name); err == nil && found {
				sourceDir := filepath.Join(engine.stateRoot, "operations", source.ID)
				currentVersion, _ := view.Status["currentVersion"].(string)
				if info, err := os.Lstat(sourceDir); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 &&
					currentVersion == strings.TrimPrefix(source.Target, "v") {
					view.RollbackSourceTaskID = source.ID
				}
			}
			views[index] = view
		}()
	}
	wait.Wait()
	return views
}

func (engine *Engine) Objects(ctx context.Context) []model.ManagedObjectView {
	return engine.objectViews(ctx, engine.catalog.ObjectNames())
}

func (engine *Engine) ObjectsForActor(
	ctx context.Context,
	actor string,
) ([]model.ManagedObjectView, error) {
	names, err := engine.authorizedObjectNames(ctx, actor, engine.catalog.ObjectNames())
	if err != nil {
		return nil, err
	}
	return engine.objectViews(ctx, names), nil
}

func (engine *Engine) objectViews(ctx context.Context, names []string) []model.ManagedObjectView {
	views := make([]model.ManagedObjectView, 0, len(names))
	for _, name := range names {
		object, _ := engine.catalog.Object(name)
		view := engine.objectView(ctx, object)
		views = append(views, view)
	}
	return views
}

func (engine *Engine) AutomaticTasks(ctx context.Context) []model.AutomaticTaskView {
	return engine.automaticTaskViews(ctx, engine.catalog.AutomaticTaskNames())
}

func (engine *Engine) AutomaticTasksForActor(
	ctx context.Context,
	actor string,
) ([]model.AutomaticTaskView, error) {
	names, err := engine.authorizedObjectNames(ctx, actor, engine.catalog.AutomaticTaskNames())
	if err != nil {
		return nil, err
	}
	return engine.automaticTaskViews(ctx, names), nil
}

func (engine *Engine) automaticTaskViews(ctx context.Context, names []string) []model.AutomaticTaskView {
	views := make([]model.AutomaticTaskView, 0, len(names))
	for _, name := range names {
		object := engine.catalog.AutomaticTasks[name]
		base := engine.objectView(ctx, object)
		views = append(views, model.AutomaticTaskView{
			ManagedObjectView: base,
			Schedule:          object.AutomaticTask.Schedule,
			ScheduleSource:    object.AutomaticTask.ScheduleSource,
			FreshnessSeconds:  object.AutomaticTask.FreshnessSeconds,
		})
	}
	return views
}

func (engine *Engine) authorizedObjectNames(
	ctx context.Context,
	actor string,
	names []string,
) ([]string, error) {
	if !actorPattern.MatchString(actor) {
		return nil, authorizationError{message: "操作者标识无效"}
	}
	result := make([]string, 0, len(names))
	for _, name := range names {
		object, _ := engine.catalog.Object(name)
		if err := engine.authorize(ctx, actor, model.PermissionRead, object.ObjectID); err == nil {
			result = append(result, name)
		} else if !isAuthorizationError(err) {
			return nil, err
		}
	}
	return result, nil
}

func (engine *Engine) objectView(ctx context.Context, object model.ServiceDefinition) model.ManagedObjectView {
	view := model.ManagedObjectView{
		Name: object.Name, ObjectID: object.ObjectID, Metadata: object.Metadata,
		DisplayName: object.DisplayName, Description: object.Description, Actions: engine.exposedActions(object),
	}
	view.TenantID, view.ServerID = object.TenantID, object.ServerID
	status, err := engine.inspect(ctx, object)
	if err != nil {
		view.StatusError = redactText(err.Error())
	} else {
		view.Status = status
	}
	if state, found, stateErr := engine.store.GetServiceState(ctx, object.Name); stateErr == nil && found {
		view.State = &state
		view.Drift = state.Drift
	}
	if active, found, err := engine.store.ActiveTask(ctx, object.Name); err == nil && found {
		view.ActiveTaskID = active.ID
	}
	return view
}

func (engine *Engine) exposedActions(service model.ServiceDefinition) map[string]model.ActionDefinition {
	result := make(map[string]model.ActionDefinition, len(service.Actions)+5)
	for name, action := range service.Actions {
		result[name] = action
	}
	for _, name := range []string{"enter-maintenance", "drain", "start", "stop", "resume-traffic"} {
		if _, exists := result[name]; exists {
			continue
		}
		if action, ok := engine.lifecycleAction(service, name); ok {
			result[name] = action
		}
	}
	return result
}
