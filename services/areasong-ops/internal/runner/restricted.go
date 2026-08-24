package runner

import (
	"context"
	"errors"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const maxTerminalOutputBytes = 1 << 20

func (engine *Engine) TerminalCommands(
	ctx context.Context,
	actor string,
) ([]model.TerminalCommand, error) {
	if err := engine.authorizePlatform(ctx, actor, model.PermissionRead, "terminal"); err != nil {
		return nil, err
	}
	if engine.catalog.Terminal == nil || !engine.catalog.Terminal.Enabled {
		return nil, errors.New("受限终端尚未启用")
	}
	names := make([]string, 0, len(engine.catalog.Terminal.Commands))
	for name := range engine.catalog.Terminal.Commands {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]model.TerminalCommand, 0, len(names))
	for _, name := range names {
		result = append(result, engine.catalog.Terminal.Commands[name])
	}
	return result, nil
}

func (engine *Engine) RunTerminal(
	ctx context.Context,
	actor string,
	request model.TerminalStartRequest,
) (model.TerminalOutput, error) {
	command, err := engine.validateTerminalRequest(ctx, actor, request)
	if err != nil {
		return model.TerminalOutput{}, err
	}
	session, created, err := engine.reserveTerminalSession(ctx, actor, request, command)
	if err != nil {
		return model.TerminalOutput{}, err
	}
	if !created {
		return terminalOutput(session), nil
	}
	output := engine.executeTerminal(ctx, command, session)
	if err := engine.store.CompleteTerminalSession(
		context.WithoutCancel(ctx), session.ID, output.State, output.ExitCode, output.Output,
	); err != nil {
		return output, err
	}
	engine.auditTerminalCommand(actor, request, command, output)
	return output, nil
}

func (engine *Engine) validateTerminalRequest(
	ctx context.Context,
	actor string,
	request model.TerminalStartRequest,
) (model.TerminalCommand, error) {
	policy := engine.catalog.Terminal
	if policy == nil || !policy.Enabled {
		return model.TerminalCommand{}, errors.New("受限终端尚未启用")
	}
	command, ok := policy.Commands[request.Command]
	if !ok {
		return model.TerminalCommand{}, errors.New("终端命令不在白名单")
	}
	permission := model.PermissionInspect
	if !command.ReadOnly {
		permission = model.PermissionBreakGlass
		if request.Confirmation != "执行受限命令 "+request.Command {
			return model.TerminalCommand{}, errors.New("非只读命令需要精确确认短语")
		}
	}
	if !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.TerminalCommand{}, errors.New("终端请求幂等键无效")
	}
	if strings.TrimSpace(request.ObjectID) == "" {
		return model.TerminalCommand{}, errors.New("终端请求缺少受管对象")
	}
	if err := engine.authorize(ctx, actor, permission, request.ObjectID); err != nil {
		return model.TerminalCommand{}, err
	}
	return command, nil
}

func (engine *Engine) reserveTerminalSession(
	ctx context.Context,
	actor string,
	request model.TerminalStartRequest,
	command model.TerminalCommand,
) (model.TerminalSession, bool, error) {
	id, err := newUUID()
	if err != nil {
		return model.TerminalSession{}, false, err
	}
	timeout := engine.terminalTimeout(command)
	now := time.Now().UTC()
	session := model.TerminalSession{
		ID: id, IdempotencyKey: request.IdempotencyKey,
		RequestDigest: digestText(strings.Join([]string{actor, request.ObjectID, request.Command}, "\x00")),
		ObjectID:      request.ObjectID, Command: request.Command, State: "running",
		ActorHash: actor, CreatedAt: now, ExpiresAt: now.Add(timeout),
	}
	return engine.store.ReserveTerminalSession(ctx, session)
}

func (engine *Engine) terminalTimeout(command model.TerminalCommand) time.Duration {
	seconds := command.TimeoutSeconds
	if maximum := engine.catalog.Terminal.MaxSessionSeconds; seconds > maximum {
		seconds = maximum
	}
	return time.Duration(seconds) * time.Second
}

func (engine *Engine) executeTerminal(
	ctx context.Context,
	command model.TerminalCommand,
	session model.TerminalSession,
) model.TerminalOutput {
	runCtx, cancel := context.WithDeadline(ctx, session.ExpiresAt)
	defer cancel()
	cmd := exec.CommandContext(runCtx, command.Executable, command.Arguments...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "OPS_ENV=production"}
	cmd.Dir = "/"
	rawOutput, runErr := cmd.CombinedOutput()
	state, exitCode := terminalExit(runCtx, runErr)
	if len(rawOutput) > maxTerminalOutputBytes {
		rawOutput = rawOutput[:maxTerminalOutputBytes]
	}
	return model.TerminalOutput{
		SessionID: session.ID, ExitCode: exitCode,
		Output: redactText(string(rawOutput)), State: state,
	}
}

func terminalExit(ctx context.Context, runErr error) (string, int) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timed_out", 124
	}
	if runErr == nil {
		return "succeeded", 0
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return "failed", exitErr.ExitCode()
	}
	return "failed", 1
}

func terminalOutput(session model.TerminalSession) model.TerminalOutput {
	return model.TerminalOutput{
		SessionID: session.ID, ExitCode: session.ExitCode,
		Output: session.Output, State: session.State,
	}
}

func (engine *Engine) auditTerminalCommand(
	actor string,
	request model.TerminalStartRequest,
	command model.TerminalCommand,
	output model.TerminalOutput,
) {
	_, _ = engine.store.AppendAudit(context.Background(), model.AuditEntry{
		ActorHash: actor, Event: "terminal.command", Resource: request.ObjectID,
		Outcome: output.State, Detail: map[string]any{
			"command": request.Command, "readOnly": command.ReadOnly,
			"sessionId": output.SessionID, "exitCode": output.ExitCode,
		},
	})
}
