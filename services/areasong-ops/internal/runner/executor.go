package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
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
	// AdapterKind is an internal routing decision. It is never accepted from
	// an HTTP request or task target, because selecting an executable is a
	// control-plane trust boundary.
	AdapterKind string
}

const (
	adapterKindService = "service"
	adapterKindTraffic = "traffic"
)

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
	adapterPath := input.Service.Adapter
	action := input.Action
	environment := commandEnvironment(input)
	if input.AdapterKind == adapterKindTraffic {
		if input.Service.TrafficPolicy == nil || input.Target != "" || input.SourceDir != "" {
			return model.AdapterResult{}, errors.New("流量适配器调用合同无效")
		}
		adapterPath = config.TrafficAdapterPath
		policy := *input.Service.TrafficPolicy
		policy.AdapterPath = config.TrafficAdapterPath
		policyJSON, err := json.Marshal(policy)
		if err != nil {
			return model.AdapterResult{}, fmt.Errorf("流量策略序列化失败: %w", err)
		}
		environment = append(environment, "OPS_TRAFFIC_POLICY_JSON="+string(policyJSON))
		// The traffic adapter uses a distinct inspect action so application
		// inspect and Nginx traffic inspect cannot be confused in logs/contracts.
		if input.Action == "inspect" {
			action = "traffic"
		}
	}
	if adapterPath == "" {
		return model.AdapterResult{}, errors.New("适配器路径为空")
	}
	command := exec.CommandContext(ctx, adapterPath, action, input.Phase,
		input.OperationDir, input.Target, input.SourceDir)
	command.Env = append(command.Environ(), environment...)
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
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return model.AdapterResult{}, fmt.Errorf("适配器阶段 %s 返回了多余输出", input.Phase)
	}
	contractAction := input.Action
	if input.AdapterKind == adapterKindTraffic && input.Action == "inspect" {
		contractAction = "traffic"
	}
	if input.Service.AdapterContractVersion >= 2 &&
		(result.SchemaVersion != 2 || result.Action != contractAction || result.Phase != input.Phase) {
		return model.AdapterResult{}, fmt.Errorf("适配器阶段 %s 返回契约身份不匹配", input.Phase)
	}
	if input.Service.AdapterContractVersion < 2 && result.SchemaVersion != 0 &&
		(result.SchemaVersion != 2 || result.Action != contractAction || result.Phase != input.Phase) {
		return model.AdapterResult{}, fmt.Errorf("适配器阶段 %s 返回契约身份不匹配", input.Phase)
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

func commandEnvironment(input ExecuteInput) []string {
	return []string{"OPS_SERVICE_NAME=" + input.Service.Name}
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
