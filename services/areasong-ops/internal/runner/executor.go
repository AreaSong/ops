package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const adapterOutputLimit = 1 << 20

type ExecuteInput struct {
	Service      model.ServiceDefinition
	Action       string
	Phase        string
	OperationDir string
	Target       string
	SourceDir    string
}

type Executor interface {
	Execute(context.Context, ExecuteInput) (model.AdapterResult, error)
}

type CommandExecutor struct{}

type cappedBuffer struct {
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func newCappedBuffer(limit int) *cappedBuffer {
	return &cappedBuffer{remaining: limit}
}

func (buffer *cappedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	if len(value) > buffer.remaining {
		value = value[:buffer.remaining]
		buffer.truncated = true
	}
	if len(value) > 0 {
		_, _ = buffer.buffer.Write(value)
		buffer.remaining -= len(value)
	}
	return original, nil
}

func (buffer *cappedBuffer) String() string {
	return buffer.buffer.String()
}

func (CommandExecutor) Execute(ctx context.Context, input ExecuteInput) (model.AdapterResult, error) {
	stdout := newCappedBuffer(adapterOutputLimit)
	stderr := newCappedBuffer(adapterOutputLimit)
	command := exec.CommandContext(ctx, input.Service.Adapter, input.Action, input.Phase,
		input.OperationDir, input.Target, input.SourceDir)
	command.Env = append(command.Environ(), "OPS_SERVICE_NAME="+input.Service.Name)
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return model.AdapterResult{}, fmt.Errorf("适配器阶段 %s 超时", input.Phase)
	}
	if err != nil {
		message := lastNonEmptyLine(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return model.AdapterResult{}, fmt.Errorf("适配器阶段 %s 失败: %s", input.Phase, redactText(message))
	}
	if stdout.truncated {
		return model.AdapterResult{}, fmt.Errorf("适配器阶段 %s 输出超过限制", input.Phase)
	}
	var result model.AdapterResult
	decoder := json.NewDecoder(strings.NewReader(stdout.String()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return model.AdapterResult{}, fmt.Errorf("适配器阶段 %s 返回无效 JSON: %w", input.Phase, err)
	}
	if !result.OK || strings.TrimSpace(result.Summary) == "" {
		return model.AdapterResult{}, fmt.Errorf("适配器阶段 %s 返回失败契约", input.Phase)
	}
	result.Summary = redactText(result.Summary)
	if result.Data != nil {
		result.Data = redactValue(result.Data).(map[string]any)
	}
	return result, nil
}

func lastNonEmptyLine(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if line := strings.TrimSpace(lines[index]); line != "" {
			return line
		}
	}
	return ""
}
