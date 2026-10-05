package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// 包内能力不由 catalog/HTTP 自报实现；CommandExecutor 尚无已证明 profile。
// 只有经过独立审阅的实现才可证明实际目标闭包及每个检查调用停止。
type planInspectionExecutor interface {
	resolvePlanInspectionScope(context.Context, model.ServiceDefinition, model.ActionDefinition, releaseScopeInput) (model.ReleaseScopeDefinition, error)
	executePlanInspection(context.Context, ExecuteInput) inspectionCall
}

type inspectionCall struct {
	Result         model.AdapterResult
	Err            error
	Settled        <-chan struct{}
	EvidenceDigest string
}

func (engine *Engine) releaseAuthority(ctx context.Context, actor, objectID string) (model.ReleaseAuthority, error) {
	policy, before, err := engine.effectiveAccessPolicy(ctx)
	if err != nil {
		return model.ReleaseAuthority{}, err
	}
	if policy == nil || !policy.Enforced {
		return model.ReleaseAuthority{}, model.ErrReleaseLifecycle
	}
	if err = engine.authorize(ctx, actor, permissionForAction("update"), objectID); err != nil {
		return model.ReleaseAuthority{}, err
	}
	_, after, err := engine.effectiveAccessPolicy(ctx)
	if err != nil {
		return model.ReleaseAuthority{}, err
	}
	if before.Version != after.Version || before.Digest != after.Digest {
		return model.ReleaseAuthority{}, model.ErrReleaseLifecycle
	}
	principal, ok := policy.Principals[actor]
	if !ok {
		return model.ReleaseAuthority{}, model.ErrReleaseLifecycle
	}
	tenant := principal.TenantID
	if tenant == "" {
		tenant = policy.DefaultTenant
	}
	return model.ReleaseAuthority{ObjectID: objectID, ActorHash: actor, TenantID: tenant, Permission: permissionForAction("update"), PolicyVersion: before.Version, PolicyDigest: before.Digest}, nil
}

func (engine *Engine) resolveReleaseScope(ctx context.Context, service model.ServiceDefinition, action model.ActionDefinition, inputs ...releaseScopeInput) ([]model.ReleaseTargetObject, string, error) {
	profile, ok := engine.executor.(planInspectionExecutor)
	if !ok || engine.remoteDispatch || service.ReleaseScope == nil || service.Metadata.Type != "service" || service.TenantID == "" || service.ServerID == "" {
		return nil, "", model.ErrReleaseNotIntegrated
	}
	actual, err := profile.resolvePlanInspectionScope(ctx, service, action, scopeInput(inputs))
	if err != nil {
		return nil, "", err
	}
	if coordinator, ok := profile.(releaseCoordinatorVerifier); ok {
		if err = coordinator.verifyReleaseCoordinator(ctx, releaseCoordinatorInput{manager: engine.alertmanager, stateRoot: engine.stateRoot, backupRoot: engine.backupRoot}); err != nil {
			return nil, "", err
		}
	}
	declared, err := model.CanonicalReleaseScope(*service.ReleaseScope)
	if err != nil {
		return nil, "", err
	}
	verified, err := model.CanonicalReleaseScope(actual)
	if err != nil || verified != declared {
		return nil, "", model.ErrReleaseLifecycle
	}
	for path, digest := range actual.ImplementationDigests {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, "", model.ErrReleaseLifecycle
		}
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() {
			return nil, "", model.ErrReleaseLifecycle
		}
		actualDigest, e := fileSHA256(path)
		if e != nil || actualDigest != digest {
			return nil, "", model.ErrReleaseLifecycle
		}
	}
	objects := make(map[string]model.ServiceDefinition)
	objects[service.ObjectID] = service
	for _, resource := range actual.Resources {
		object, exists := engine.releaseObjectByID(resource.ObjectID)
		if !exists || object.ObjectID != resource.ObjectID || object.TenantID == "" || object.ServerID == "" {
			return nil, "", model.ErrReleaseLifecycle
		}
		objects[object.ObjectID] = object
	}
	keys := make([]string, 0, len(objects))
	for key := range objects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	targets := make([]model.ReleaseTargetObject, 0, len(keys))
	definitions := make([]model.ServiceDefinition, 0, len(keys))
	contracts := make(map[string]int)
	for _, key := range keys {
		o := objects[key]
		targets = append(targets, model.ReleaseTargetObject{ObjectID: o.ObjectID, TenantID: o.TenantID, ServerID: o.ServerID})
		definitions = append(definitions, o)
		contracts[key] = o.AdapterContractVersion
	}
	raw, err := json.Marshal(struct {
		Scope     string
		Objects   []model.ServiceDefinition
		Action    model.ActionDefinition
		Contracts map[string]int
	}{verified, definitions, action, contracts})
	return targets, model.WorkDigest(string(raw)), err
}

func (engine *Engine) verifyReleaseScope(ctx context.Context, service model.ServiceDefinition, action model.ActionDefinition, binding model.ReleaseLifecycleBinding, inputs ...releaseScopeInput) error {
	current, ok := engine.catalog.Services[service.Name]
	if !ok {
		return model.ErrReleaseLifecycle
	}
	currentAction, ok := current.Actions[action.Name]
	if !ok || !reflect.DeepEqual(currentAction, action) {
		return model.ErrReleaseLifecycle
	}
	objects, digest, err := engine.resolveReleaseScope(ctx, current, currentAction, inputs...)
	if err != nil {
		return err
	}
	if digest != binding.ScopeDigest || !reflect.DeepEqual(objects, binding.TargetObjects) {
		return model.ErrReleaseLifecycle
	}
	return nil
}

func (engine *Engine) releaseObjectByID(id string) (model.ServiceDefinition, bool) {
	var found model.ServiceDefinition
	count := 0
	for _, name := range engine.catalog.ObjectNames() {
		object, _ := engine.catalog.Object(name)
		if object.ObjectID == id {
			found = object
			count++
		}
	}
	return found, count == 1
}
