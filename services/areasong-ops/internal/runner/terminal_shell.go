package runner

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const maxTerminalShellInputBytes = 64 << 10

func (engine *Engine) TerminalShellPlans(ctx context.Context, actor string) ([]model.TerminalShellPlan, error) {
	if engine.catalog.Terminal == nil || !engine.catalog.Terminal.Enabled || !engine.catalog.Terminal.BreakGlass {
		return nil, errors.New("紧急终端尚未启用")
	}
	items, err := engine.store.ListTerminalShellPlans(ctx, 100)
	if err != nil {
		return nil, err
	}
	result := make([]model.TerminalShellPlan, 0, len(items))
	for _, item := range items {
		if engine.authorize(ctx, actor, model.PermissionBreakGlass, item.ObjectID) == nil {
			result = append(result, item)
		}
	}
	return result, nil
}

func (engine *Engine) CreateTerminalShellPlan(
	ctx context.Context, actor string, request model.TerminalShellPlanRequest,
) (model.TerminalShellPlan, bool, error) {
	policy := engine.catalog.Terminal
	if policy == nil || !policy.Enabled || !policy.BreakGlass {
		return model.TerminalShellPlan{}, false, errors.New("紧急终端尚未启用")
	}
	if !actorPattern.MatchString(actor) || !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.TerminalShellPlan{}, false, errors.New("紧急终端请求标识无效")
	}
	if err := engine.authorize(ctx, actor, model.PermissionBreakGlass, request.ObjectID); err != nil {
		return model.TerminalShellPlan{}, false, err
	}
	if len(request.Input) == 0 || len(request.Input) > maxTerminalShellInputBytes || strings.ContainsRune(request.Input, '\x00') {
		return model.TerminalShellPlan{}, false, errors.New("紧急终端输入为空、过大或包含非法字符")
	}
	expected := "申请紧急终端 " + request.ObjectID
	if request.Confirmation != expected {
		return model.TerminalShellPlan{}, false, errors.New("紧急终端申请确认短语不匹配")
	}
	id, err := newUUID()
	if err != nil {
		return model.TerminalShellPlan{}, false, err
	}
	digest := digestText(request.Input)
	now := time.Now().UTC()
	plan := model.TerminalShellPlan{
		ID: id, ObjectID: request.ObjectID, State: "pending_approval", ActorHash: actor,
		InputDigest: digest, ConfirmationPhrase: fmt.Sprintf("批准紧急终端 %s %s", id, shortDigest(digest)),
		CreatedAt: now, ExpiresAt: now.Add(time.Duration(policy.MaxSessionSeconds) * time.Second),
	}
	requestDigest := digestText(strings.Join([]string{actor, request.ObjectID, digest, request.Confirmation}, "\x00"))
	stored, created, err := engine.store.CreateTerminalShellPlan(ctx, plan, request.IdempotencyKey, requestDigest)
	if err != nil {
		return model.TerminalShellPlan{}, false, err
	}
	if created {
		_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{ActorHash: actor, Event: "terminal.shell.requested", Resource: id, Outcome: "pending_approval", Detail: map[string]any{"objectId": request.ObjectID, "inputDigest": digest}})
	}
	return stored, created, nil
}

func (engine *Engine) ApproveTerminalShellPlan(
	ctx context.Context, actor, id string, request model.TerminalShellApprovalRequest,
) (model.TerminalShellPlan, error) {
	if !actorPattern.MatchString(actor) || !uuidPattern.MatchString(id) {
		return model.TerminalShellPlan{}, errors.New("紧急终端批准标识无效")
	}
	plan, err := engine.store.GetTerminalShellPlan(ctx, id)
	if err != nil {
		return model.TerminalShellPlan{}, err
	}
	if err := engine.authorize(ctx, actor, model.PermissionBreakGlass, plan.ObjectID); err != nil {
		return model.TerminalShellPlan{}, err
	}
	approved, err := engine.store.ApproveTerminalShellPlan(ctx, id, actor, request.Confirmation)
	if err != nil {
		return model.TerminalShellPlan{}, err
	}
	_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{ActorHash: actor, Event: "terminal.shell.approved", Resource: id, Outcome: "approved", Detail: map[string]any{"objectId": plan.ObjectID, "inputDigest": plan.InputDigest}})
	return approved, nil
}

func (engine *Engine) ExecuteTerminalShellPlan(
	ctx context.Context, actor, id string, request model.TerminalShellExecuteRequest,
) (model.TerminalShellPlan, error) {
	policy := engine.catalog.Terminal
	if policy == nil || !policy.Enabled || !policy.BreakGlass {
		return model.TerminalShellPlan{}, errors.New("紧急终端尚未启用")
	}
	if !actorPattern.MatchString(actor) || !uuidPattern.MatchString(id) || !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.TerminalShellPlan{}, errors.New("紧急终端执行标识无效")
	}
	if len(request.Input) == 0 || len(request.Input) > maxTerminalShellInputBytes || strings.ContainsRune(request.Input, '\x00') {
		return model.TerminalShellPlan{}, errors.New("紧急终端输入为空、过大或包含非法字符")
	}
	plan, err := engine.store.GetTerminalShellPlan(ctx, id)
	if err != nil {
		return model.TerminalShellPlan{}, err
	}
	if err := engine.authorize(ctx, actor, model.PermissionBreakGlass, plan.ObjectID); err != nil {
		return model.TerminalShellPlan{}, err
	}
	plan, started, err := engine.store.StartTerminalShellPlan(ctx, id, actor, request.IdempotencyKey, digestText(request.Input))
	if err != nil {
		return plan, err
	}
	if !started {
		return plan, nil
	}
	deadline := plan.ExpiresAt
	if remaining := time.Until(deadline); remaining > time.Duration(policy.MaxSessionSeconds)*time.Second {
		deadline = time.Now().Add(time.Duration(policy.MaxSessionSeconds) * time.Second)
	}
	runCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	command := exec.CommandContext(runCtx, policy.ShellExecutable, "--noprofile", "--norc", "-lc", request.Input)
	command.Dir = policy.ShellWorkingDir
	command.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/nonexistent", "OPS_ENV=production", "LANG=C.UTF-8"}
	rawOutput, runErr := command.CombinedOutput()
	if len(rawOutput) > maxTerminalOutputBytes {
		rawOutput = rawOutput[:maxTerminalOutputBytes]
	}
	state, exitCode := terminalExit(runCtx, runErr)
	output, errorText := redactText(string(rawOutput)), ""
	if runErr != nil {
		errorText = redactText(runErr.Error())
	}
	if finishErr := engine.store.FinishTerminalShellPlan(context.WithoutCancel(ctx), id, state, exitCode, output, errorText); finishErr != nil {
		_ = engine.store.FinishTerminalShellPlan(context.Background(), id, "needs_attention", exitCode, output, "终端命令已执行但状态收口失败")
		return plan, fmt.Errorf("紧急终端命令已执行但状态收口失败: %w", finishErr)
	}
	finished := time.Now().UTC()
	plan.State, plan.ExitCode, plan.Output, plan.Error, plan.FinishedAt = state, exitCode, output, errorText, &finished
	_, _ = engine.store.AppendAudit(context.Background(), model.AuditEntry{ActorHash: actor, Event: "terminal.shell.executed", Resource: id, Outcome: state, Detail: map[string]any{"objectId": plan.ObjectID, "inputDigest": plan.InputDigest, "exitCode": exitCode}})
	if runErr != nil {
		return plan, errors.New(errorText)
	}
	return plan, nil
}

func shortDigest(value string) string {
	if len(value) <= 22 {
		return value
	}
	return value[:22]
}
