package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

type ExtensionRuntime interface {
	Execute(
		context.Context,
		config.ExtensionPolicy,
		model.ExtensionManifest,
		[]byte,
		[]byte,
	) (string, int, error)
}

type wasmExtensionRuntime struct{}

const extensionRuntimeContractVersion = "wasi-preview1-stdio-v1"

func (wasmExtensionRuntime) Execute(
	ctx context.Context,
	policy config.ExtensionPolicy,
	manifest model.ExtensionManifest,
	artifact, input []byte,
) (string, int, error) {
	if policy.Sandbox != "wasm" || manifest.Entrypoint != "_start" {
		return "", -1, errors.New("扩展不符合 WASM 执行合同")
	}
	runtimeConfig := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(policy.MaxMemoryPages).
		WithCloseOnContextDone(true)
	runtime := wazero.NewRuntimeWithConfig(ctx, runtimeConfig)
	defer runtime.Close(ctx)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		return "", -1, fmt.Errorf("初始化 WASM 沙箱失败: %w", err)
	}
	compiled, err := runtime.CompileModule(ctx, artifact)
	if err != nil {
		return "", -1, fmt.Errorf("编译 WASM 扩展失败: %w", err)
	}
	defer compiled.Close(ctx)
	if err := validateWASMImports(compiled); err != nil {
		return "", -1, err
	}
	output := &boundedExtensionOutput{limit: policy.MaxOutputBytes}
	moduleConfig := wazero.NewModuleConfig().
		WithName("").
		WithStartFunctions(manifest.Entrypoint).
		WithStdin(bytes.NewReader(input)).
		WithStdout(output).
		WithStderr(output)
	_, runErr := runtime.InstantiateModule(ctx, compiled, moduleConfig)
	cleanOutput := strings.TrimSpace(output.String())
	if output.exceeded {
		return cleanOutput, -1, errors.New("扩展输出超过策略上限")
	}
	if runErr == nil {
		return cleanOutput, 0, nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return cleanOutput, 124, errors.New("扩展执行超时")
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return cleanOutput, -1, errors.New("扩展执行已取消")
	}
	var exitErr *sys.ExitError
	if errors.As(runErr, &exitErr) {
		return cleanOutput, int(exitErr.ExitCode()), fmt.Errorf("扩展退出码为 %d", exitErr.ExitCode())
	}
	return cleanOutput, -1, fmt.Errorf("执行 WASM 扩展失败: %w", runErr)
}

func validateWASMImports(compiled wazero.CompiledModule) error {
	// stdin is the only input channel exposed to an extension. fd_read is safe
	// here because the module has no filesystem, network, environment, or other
	// host descriptors; all output remains bounded below.
	allowedWASI := map[string]struct{}{"fd_read": {}, "fd_write": {}, "proc_exit": {}}
	for _, definition := range compiled.ImportedFunctions() {
		moduleName, name, imported := definition.Import()
		if !imported || moduleName != wasi_snapshot_preview1.ModuleName {
			return errors.New("WASM 扩展声明了未授权的宿主函数")
		}
		if _, ok := allowedWASI[name]; !ok {
			return fmt.Errorf("WASM 扩展导入了未授权的 WASI 函数: %s", name)
		}
	}
	if len(compiled.ImportedMemories()) != 0 {
		return errors.New("WASM 扩展不能导入宿主内存")
	}
	return nil
}

type boundedExtensionOutput struct {
	buffer   bytes.Buffer
	limit    int64
	exceeded bool
}

func (output *boundedExtensionOutput) Write(value []byte) (int, error) {
	remaining := output.limit - int64(output.buffer.Len())
	if remaining <= 0 {
		output.exceeded = true
		return 0, errors.New("扩展输出超过策略上限")
	}
	if int64(len(value)) > remaining {
		_, _ = output.buffer.Write(value[:remaining])
		output.exceeded = true
		return int(remaining), errors.New("扩展输出超过策略上限")
	}
	return output.buffer.Write(value)
}

func (output *boundedExtensionOutput) String() string {
	return output.buffer.String()
}

var _ io.Writer = (*boundedExtensionOutput)(nil)
