package runner

import (
	"context"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// 能力投影只用于提示；不持久化，不从HTTP接收，更不授予执行权。
type releasePlanView struct {
	model.ReleasePlan
	ExecutionAvailable bool `json:"executionAvailable"`
	ClosureAvailable   bool `json:"closureAvailable"`
}

func (engine *Engine) releasePlanView(ctx context.Context, plan model.ReleasePlan) releasePlanView {
	view := releasePlanView{ReleasePlan: plan}
	if _, _, _, err := engine.verifyExecutionProfile(ctx, plan); err == nil {
		view.ExecutionAvailable = true
	}
	l := engine.releaseLease(plan.TaskID)
	if l != nil && l.mu.TryLock() {
		view.ClosureAvailable = l.executionSettled && !l.uncertain && l.heartbeatStopped && l.locksReleased && l.blockedAttempt == ""
		l.mu.Unlock()
	}
	return view
}
