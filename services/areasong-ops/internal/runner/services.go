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
				Name: service.Name, DisplayName: service.DisplayName,
				Description: service.Description, Actions: service.Actions,
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
