package runner

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// 原子/不可变 bundle、工具及环境边界由后端证明，不能由 manifest 中的 true 自授信。
// 观测含所有资源的实际身份/归属及拓扑；业务数据内容不进入 scope。
type sub2apiObservation struct {
	ManifestDigest, ImplementationDigest, EnvironmentDigest                      string
	Resources                                                                    map[string]sub2apiResource
	State                                                                        sub2apiState
	ImmutableInputs, ClosedDependencies, IsolatedEnvironment, ExclusiveResources bool
}

type sub2apiInspectionBackend interface {
	observe(context.Context, sub2apiManifest) (sub2apiObservation, error)
	inspect(context.Context, sub2apiCallRequest) (sub2apiCompletion, error)
	commitReceipt(context.Context, string, []byte) ([]byte, error)
}

// 实现检查接口不会自动获得运行接口。B1 只有 _test.go 提供这两种后端。
type sub2apiExecutionBackend interface {
	runSub2API(context.Context, sub2apiCallRequest) (sub2apiCompletion, error)
}

type sub2apiInspectionProfile struct {
	manifestPath, manifestDigest string
	backend                      sub2apiInspectionBackend
}
type sub2apiExecutionProfile struct {
	*sub2apiInspectionProfile
	execution sub2apiExecutionBackend
}

func (*sub2apiInspectionProfile) Execute(context.Context, ExecuteInput) (model.AdapterResult, error) {
	return model.AdapterResult{}, model.ErrReleaseNotIntegrated
}

func (p *sub2apiInspectionProfile) load(ctx context.Context, service model.ServiceDefinition, target string) (sub2apiManifest, sub2apiObservation, error) {
	var m sub2apiManifest
	var o sub2apiObservation
	if p == nil || p.backend == nil {
		return m, o, sub2apiHostExecutionError()
	}
	b, err := sub2apiRegular(p.manifestPath)
	if err != nil || model.WorkDigest(string(b)) != p.manifestDigest || decodeSub2API(b, &m) != nil || m.validate(service, target) != nil {
		return m, o, errSub2API
	}
	o, err = p.backend.observe(ctx, m)
	if err != nil || ctx.Err() != nil || o.ManifestDigest != p.manifestDigest || o.ImplementationDigest != sub2apiDigest(m.Files) ||
		o.EnvironmentDigest != sub2apiDigest(m.Environment) || !reflect.DeepEqual(o.Resources, m.Resources) ||
		!o.ImmutableInputs || !o.ClosedDependencies || !o.IsolatedEnvironment || !o.ExclusiveResources {
		return m, o, errSub2API
	}
	// 可变配置不放进 ImplementationDigests；字节必须与本阶段已证明的状态相同。
	for path, digest := range map[string]string{m.Runtime.ControlledCompose: o.State.Controlled, m.Runtime.RuntimeCompose: o.State.Runtime} {
		b, err := sub2apiRegular(path)
		if err != nil || model.WorkDigest(string(b)) != digest {
			return m, o, errSub2API
		}
	}
	return m, o, nil
}

func (p *sub2apiInspectionProfile) resolvePlanInspectionScope(ctx context.Context, s model.ServiceDefinition, a model.ActionDefinition, in releaseScopeInput) (model.ReleaseScopeDefinition, error) {
	if a.Name != "update" || !reflect.DeepEqual(a.Steps, sub2apiPhases) {
		return model.ReleaseScopeDefinition{}, errSub2API
	}
	m, o, err := p.load(ctx, s, in.target)
	if err != nil {
		return model.ReleaseScopeDefinition{}, err
	}
	expected := m.Before
	if in.call != nil {
		in.call.mu.Lock()
		defer in.call.mu.Unlock()
		progress, err := p.progress(in.call)
		if err != nil || in.call.target != in.target {
			return model.ReleaseScopeDefinition{}, errSub2API
		}
		if progress.last != nil {
			expected = progress.last.After
		}
		if m.partial(expected) && (in.phase != "rollback" || !progress.failed || progress.last.Request.Phase != "apply") {
			return model.ReleaseScopeDefinition{}, errSub2API
		}
	}
	if o.State != expected {
		return model.ReleaseScopeDefinition{}, errSub2API
	}
	return m.scope(p.manifestPath, p.manifestDigest), nil
}

func (p *sub2apiExecutionProfile) resolveReleaseExecutionScope(ctx context.Context, s model.ServiceDefinition, a model.ActionDefinition, in releaseScopeInput) (model.ReleaseScopeDefinition, error) {
	if p == nil || p.execution == nil {
		return model.ReleaseScopeDefinition{}, sub2apiHostExecutionError()
	}
	return p.resolvePlanInspectionScope(ctx, s, a, in)
}

func (p *sub2apiInspectionProfile) executePlanInspection(ctx context.Context, in ExecuteInput) inspectionCall {
	return p.call(ctx, in, nil)
}

func (p *sub2apiExecutionProfile) executeReleaseCall(ctx context.Context, in ExecuteInput) inspectionCall {
	if p == nil || p.execution == nil {
		return inspectionCall{Err: sub2apiHostExecutionError()}
	}
	return p.call(ctx, in, p.execution)
}

func (p *sub2apiInspectionProfile) call(ctx context.Context, in ExecuteInput, execution sub2apiExecutionBackend) inspectionCall {
	c := in.releaseContext
	if c == nil || p == nil || p.backend == nil {
		return inspectionCall{Err: sub2apiHostExecutionError()}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := p.progress(c)
	if err != nil {
		return inspectionCall{Err: errSub2API}
	}
	if !s.acceptsPhase(in) {
		s.blocked = true
		return inspectionCall{Err: errSub2API}
	}
	m, o, err := p.load(ctx, in.Service, c.target)
	expected := m.Before
	if s.last != nil {
		expected = s.last.After
	}
	if err != nil || o.State != expected || in.SourceDir != "" || !sub2apiPath(in.OperationDir) {
		s.blocked = true
		return inspectionCall{Err: errSub2API}
	}
	if (in.Phase == "rollback" && expected != m.TargetState && !m.partial(expected)) || !validSub2APICall(in, c, m, execution) {
		s.blocked = true
		return inspectionCall{Err: errSub2API}
	}
	id, err := newUUID()
	if err != nil {
		return inspectionCall{Err: errSub2API}
	}
	req := sub2apiCallRequest{CallID: id, PreparationID: c.preparationID, PlanID: c.planID, TaskID: c.taskID, LeaseID: c.leaseID,
		ScopeDigest: c.scopeDigest, ImplementationDigest: sub2apiDigest(m.Files), Previous: s.previous(), Target: c.target,
		Action: in.Action, Phase: in.Phase, Kind: in.AdapterKind, OperationDir: in.OperationDir}
	req.InputDigest = sub2apiDigest(req)
	s.blocked = true // 取消/缺证/保存失败后，迟到调用不能重新推进。
	var completion sub2apiCompletion
	if execution == nil {
		completion, err = p.backend.inspect(ctx, req)
	} else {
		completion, err = execution.runSub2API(ctx, req)
	}
	if err != nil {
		return inspectionCall{Err: errSub2API}
	}
	return p.finishCall(ctx, m, s, req, completion)
}

func validSub2APICall(in ExecuteInput, c *releaseCallContext, m sub2apiManifest, execution sub2apiExecutionBackend) bool {
	if execution == nil {
		return in.Action == "inspect" && in.Phase == "inspect" && in.Target == "" &&
			(in.AdapterKind == adapterKindService || (in.AdapterKind == adapterKindTraffic && in.Service.TrafficPolicy != nil)) &&
			filepath.Dir(in.OperationDir) == m.Resources["inspection-root"].Selector && strings.HasPrefix(filepath.Base(in.OperationDir), ".plan-inspection-")
	}
	return c.taskID != "" && in.Action == "update" && in.Target == c.target &&
		in.AdapterKind == adapterKindService && in.OperationDir == filepath.Join(m.Resources["operation-root"].Selector, c.taskID)
}
