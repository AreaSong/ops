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
	names := engine.catalog.ServiceNames()
	views := make([]model.ServiceView, len(names))
	var wait sync.WaitGroup
	for index, name := range names {
		index, service := index, engine.catalog.Services[name]
		wait.Add(1)
		go func() {
			defer wait.Done()
			view := model.ServiceView{
				Name: service.Name, ObjectID: service.ObjectID, DisplayName: service.DisplayName,
				Metadata: service.Metadata, Description: service.Description, Actions: service.Actions,
			}
			status, err := engine.inspect(ctx, service)
			if err != nil {
				view.StatusError = redactText(err.Error())
			} else {
				view.Status = status
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
	names := engine.catalog.ObjectNames()
	views := make([]model.ManagedObjectView, 0, len(names))
	for _, name := range names {
		object, _ := engine.catalog.Object(name)
		view := engine.objectView(ctx, object)
		views = append(views, view)
	}
	return views
}

func (engine *Engine) AutomaticTasks(ctx context.Context) []model.AutomaticTaskView {
	names := engine.catalog.AutomaticTaskNames()
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

func (engine *Engine) objectView(ctx context.Context, object model.ServiceDefinition) model.ManagedObjectView {
	view := model.ManagedObjectView{
		Name: object.Name, ObjectID: object.ObjectID, Metadata: object.Metadata,
		DisplayName: object.DisplayName, Description: object.Description, Actions: object.Actions,
	}
	status, err := engine.inspect(ctx, object)
	if err != nil {
		view.StatusError = redactText(err.Error())
	} else {
		view.Status = status
	}
	if active, found, err := engine.store.ActiveTask(ctx, object.Name); err == nil && found {
		view.ActiveTaskID = active.ID
	}
	return view
}
