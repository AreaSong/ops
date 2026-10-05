package runner

import (
	"sync"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// 上下文只在登记成功后创建。lease 所有权仍由 store 校验；这里不复制 owner。
// target 已在原计划摘要中，阶段和前置回执只由 profile 在内存中推进。
type releaseCallContext struct {
	mu                                                          sync.Mutex
	preparationID, planID, taskID, leaseID, target, scopeDigest string
	sub2api                                                     *sub2apiProgress
}

type releaseScopeInput struct {
	target string
	phase  string
	call   *releaseCallContext
}

func scopeInput(inputs []releaseScopeInput) releaseScopeInput {
	if len(inputs) == 1 {
		return inputs[0]
	}
	return releaseScopeInput{}
}

func preparationCallContext(r model.PlanPreparation, contexts []*releaseCallContext) *releaseCallContext {
	if len(contexts) == 1 {
		return contexts[0]
	}
	return &releaseCallContext{preparationID: r.ID, planID: r.Request.PlanID, target: r.Request.Target, scopeDigest: r.Request.Binding.ScopeDigest}
}

func executionCallContext(l *releaseExecutionLease) *releaseCallContext {
	return &releaseCallContext{preparationID: l.plan.ApprovalSummary.Lifecycle.PreparationID,
		planID: l.plan.ID, taskID: l.taskID, leaseID: l.record.ID, target: l.plan.Target, scopeDigest: l.scopeDigest}
}

func (l *releaseExecutionLease) scopeInput() releaseScopeInput {
	return releaseScopeInput{target: l.plan.Target, call: l.callContext}
}
