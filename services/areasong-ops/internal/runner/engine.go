package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

var (
	actorPattern   = regexp.MustCompile(`^[a-f0-9]{64}$`)
	uuidPattern    = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)
	releasePattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
)

type Engine struct {
	catalog         *config.Catalog
	store           *store.Store
	executor        Executor
	broker          *Broker
	stateRoot       string
	lockMu          sync.Mutex
	locks           map[string]string
	credentialMu    sync.Mutex
	wait            sync.WaitGroup
	owner           string
	backupRoot      string
	alertmanager    Alertmanager
	credentials     CredentialRotator
	runnerUpdater   RunnerUpdateLauncher
	extensionRunner ExtensionRuntime
	composeRunner   ComposeCommandRunner
	terminalMu      sync.Mutex
	terminals       map[string]model.TerminalSession
	remoteDispatch  bool
	// lifecycleObservationSeconds is negative in production, preserving the
	// fixed 300-second lifecycle observation window. It is injectable only for
	// disposable local acceptance runners.
	lifecycleObservationSeconds int
	fleetUpdateCtx              context.Context
	fleetUpdateStop             context.CancelFunc
}

type EngineOption func(*Engine)

func WithAlertmanager(alertmanager Alertmanager) EngineOption {
	return func(engine *Engine) {
		if alertmanager != nil {
			engine.alertmanager = alertmanager
		}
	}
}

func WithCredentialRotator(rotator CredentialRotator) EngineOption {
	return func(engine *Engine) {
		if rotator != nil {
			engine.credentials = rotator
		}
	}
}

// WithBackupRoot is used by isolated runners and tests. Production entrypoints
// omit it and retain the fixed /var/backups/ops trust boundary.
func WithBackupRoot(root string) EngineOption {
	return func(engine *Engine) {
		clean := filepath.Clean(strings.TrimSpace(root))
		if filepath.IsAbs(clean) && clean != string(filepath.Separator) {
			engine.backupRoot = clean
		}
	}
}

func WithRunnerUpdateLauncher(launcher RunnerUpdateLauncher) EngineOption {
	return func(engine *Engine) {
		if launcher != nil {
			engine.runnerUpdater = launcher
		}
	}
}

func WithExtensionRuntime(runtime ExtensionRuntime) EngineOption {
	return func(engine *Engine) {
		if runtime != nil {
			engine.extensionRunner = runtime
		}
	}
}

// WithComposeRunner is intentionally injectable so the Compose state machine
// can be verified without touching a host Docker daemon. Production uses the
// fixed system Docker executable configured by NewEngineChecked.
func WithComposeRunner(runner ComposeCommandRunner) EngineOption {
	return func(engine *Engine) {
		if runner != nil {
			engine.composeRunner = runner
		}
	}
}

// WithRemoteDispatch turns the control-plane process into a dispatcher. A task
// is durably assigned before enqueue returns; no local execution goroutine is
// started while the option is enabled.
func WithRemoteDispatch(enabled bool) EngineOption {
	return func(engine *Engine) { engine.remoteDispatch = enabled }
}

// WithLifecycleObservationSeconds shortens lifecycle observation in isolated
// acceptance environments. Production entrypoints intentionally never use it.
func WithLifecycleObservationSeconds(seconds int) EngineOption {
	return func(engine *Engine) {
		if seconds >= 0 && seconds <= 60 {
			engine.lifecycleObservationSeconds = seconds
		}
	}
}

// NewEngine keeps the historical constructor shape for in-process callers.
// Production entrypoints should use NewEngineChecked so bootstrap failures are
// returned instead of being silently tolerated.
func NewEngine(
	catalog *config.Catalog,
	database *store.Store,
	executor Executor,
	stateRoot string,
	options ...EngineOption,
) *Engine {
	engine, err := NewEngineChecked(catalog, database, executor, stateRoot, options...)
	if err != nil {
		panic(err)
	}
	return engine
}

func NewEngineChecked(
	catalog *config.Catalog,
	database *store.Store,
	executor Executor,
	stateRoot string,
	options ...EngineOption,
) (*Engine, error) {
	fleetUpdateCtx, fleetUpdateStop := context.WithCancel(context.Background())
	owner, err := newUUID()
	if err != nil {
		owner = fmt.Sprintf("runner-%d", os.Getpid())
	}
	engine := &Engine{
		catalog: catalog, store: database, executor: executor, broker: NewBroker(),
		stateRoot: stateRoot, locks: make(map[string]string), owner: owner,
		backupRoot: "/var/backups/ops", alertmanager: unavailableAlertmanager{},
		credentials:                 unavailableCredentialRotator{},
		extensionRunner:             wasmExtensionRuntime{},
		composeRunner:               systemComposeCommandRunner{executable: "/usr/bin/docker"},
		terminals:                   make(map[string]model.TerminalSession),
		lifecycleObservationSeconds: -1,
		fleetUpdateCtx:              fleetUpdateCtx,
		fleetUpdateStop:             fleetUpdateStop,
	}
	for _, option := range options {
		option(engine)
	}
	if err := engine.seedAccessPolicy(); err != nil {
		return nil, fmt.Errorf("初始化访问策略失败: %w", err)
	}
	if err := engine.seedFleet(); err != nil {
		return nil, fmt.Errorf("初始化 Fleet 失败: %w", err)
	}
	if err := engine.seedAutoUpdatePolicies(); err != nil {
		return nil, fmt.Errorf("初始化自动更新策略失败: %w", err)
	}
	if err := engine.resumeBatchOperations(); err != nil {
		return nil, fmt.Errorf("恢复批量协调器失败: %w", err)
	}
	if err := engine.resumeFleetRunnerUpdates(); err != nil {
		return nil, fmt.Errorf("恢复 Runner Fleet 更新协调器失败: %w", err)
	}
	if err := engine.store.RecoverInterruptedExtensionPlans(context.Background()); err != nil {
		return nil, fmt.Errorf("恢复扩展执行状态失败: %w", err)
	}
	if err := engine.store.RecoverInterruptedComposeRevisions(context.Background()); err != nil {
		return nil, fmt.Errorf("恢复 Compose 执行状态失败: %w", err)
	}
	if err := engine.store.ExpireComposeRevisions(context.Background(), time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("收口过期 Compose 提案失败: %w", err)
	}
	return engine, nil
}

// Stop cancels restart-safe background coordinators before the caller waits
// for goroutines. A Runner self-update would otherwise wait on its own restart.
func (engine *Engine) Stop() {
	if engine.fleetUpdateStop != nil {
		engine.fleetUpdateStop()
	}
}

func (engine *Engine) seedAccessPolicy() error {
	if engine.catalog.Access == nil {
		return nil
	}
	ctx := context.Background()
	if err := engine.store.EnsureAccessDefaults(ctx); err != nil {
		return err
	}
	// Once a durable snapshot exists it is the authority. Re-reading a changed
	// catalog must not silently overwrite operator-created tenants, roles, or
	// bindings in a running production control plane.
	if _, found, err := engine.store.GetAccessPolicySnapshot(ctx); err != nil {
		return err
	} else if found {
		return nil
	}
	tenants := make([]model.Tenant, 0, len(engine.catalog.Access.Tenants))
	roles := make([]model.Role, 0, len(engine.catalog.Access.Roles))
	bindings := make([]model.RoleBinding, 0, len(engine.catalog.Access.Bindings))
	for _, tenant := range engine.catalog.Access.Tenants {
		tenants = append(tenants, tenant)
	}
	for _, role := range engine.catalog.Access.Roles {
		roles = append(roles, role)
	}
	for _, binding := range engine.catalog.Access.Bindings {
		bindings = append(bindings, binding)
	}
	if err := engine.store.ReconcileBootstrapAccess(ctx, tenants, roles, bindings); err != nil {
		return err
	}
	policyJSON, err := json.Marshal(engine.catalog.Access)
	if err != nil {
		return err
	}
	digest := digestText(string(policyJSON))
	_, _, err = engine.store.EnsureAccessPolicySnapshot(ctx, model.AccessPolicySnapshot{
		Digest: digest, PolicyJSON: string(policyJSON), ActorHash: "bootstrap",
	})
	return err
}

func (engine *Engine) seedFleet() error {
	if engine.catalog.Fleet == nil {
		return nil
	}
	ctx := context.Background()
	for _, node := range engine.catalog.Fleet.Inventory.Servers {
		if err := engine.store.UpsertServerNode(ctx, node); err != nil {
			return err
		}
	}
	for _, node := range engine.catalog.Fleet.Inventory.Runners {
		tenantID := node.TenantID
		if tenantID == "" && engine.catalog.Access != nil {
			tenantID = engine.catalog.Access.DefaultTenant
		}
		if err := engine.store.UpsertRunnerNode(ctx, node, tenantID); err != nil {
			return err
		}
	}
	return nil
}

func (engine *Engine) Broker() *Broker {
	return engine.broker
}

func (engine *Engine) CreatePreview(
	ctx context.Context,
	actorHash string,
	request model.PreviewRequest,
) (model.Preview, error) {
	if !actorPattern.MatchString(actorHash) {
		return model.Preview{}, errors.New("操作者标识无效")
	}
	service, action, err := engine.resolveAction(request.Service, request.Action, request.Target)
	if err != nil {
		return model.Preview{}, err
	}
	if err := engine.authorize(ctx, actorHash, permissionForAction(action.Name), service.ObjectID); err != nil {
		return model.Preview{}, err
	}
	if active, found, err := engine.store.ActiveTask(ctx, service.Name); err != nil {
		return model.Preview{}, err
	} else if found {
		return model.Preview{}, fmt.Errorf("服务已有活动任务: %s", active.ID)
	}
	snapshot, err := engine.inspectForAction(ctx, service, action.Name)
	if err != nil {
		return model.Preview{}, fmt.Errorf("创建预览前检查失败: %w", err)
	}
	if digest := service.PolicyDigest(); digest != "" {
		snapshot["trafficPolicyDigest"] = digest
	}
	if action.TargetMode == "controlled_rollback" {
		if err := engine.validateRollbackSource(service.Name, request.Target, snapshot); err != nil {
			return model.Preview{}, err
		}
		snapshot["rollbackSourceTaskId"] = request.Target
	}
	id, err := newUUID()
	if err != nil {
		return model.Preview{}, err
	}
	now := time.Now().UTC()
	phrase := renderConfirmation(action.ConfirmationTemplate, service.Name, request.Target)
	preview := model.Preview{
		ID: id, ActorHash: actorHash, Service: service.Name, Action: action.Name,
		Target: request.Target, Risk: action.Risk, Impact: action.Impact,
		Rollback: action.Rollback, Scope: action.Scope, Steps: action.Steps,
		RequiresConfirmation: action.Risk != model.RiskReadOnly,
		ConfirmationPhrase:   phrase, Snapshot: snapshot, CreatedAt: now,
		ExpiresAt: now.Add(5 * time.Minute),
	}
	if err := engine.store.CreatePreview(ctx, store.PreviewInput{
		Preview: preview, ConfirmationHash: store.HashConfirmation(phrase),
	}); err != nil {
		return model.Preview{}, err
	}
	_, _ = engine.store.AppendAudit(context.Background(), model.AuditEntry{
		ActorHash: actorHash, Event: "preview.created", Resource: service.Name + "/" + action.Name,
		Outcome: "accepted", Detail: map[string]any{"target": request.Target, "risk": action.Risk},
	})
	return preview, nil
}

func (engine *Engine) StartTask(
	ctx context.Context,
	actorHash string,
	request model.StartTaskRequest,
) (model.Task, bool, error) {
	if !actorPattern.MatchString(actorHash) || !uuidPattern.MatchString(request.PreviewID) ||
		!uuidPattern.MatchString(request.IdempotencyKey) {
		return model.Task{}, false, errors.New("任务请求标识无效")
	}
	preview, err := engine.store.GetPreview(ctx, request.PreviewID)
	if err != nil {
		return model.Task{}, false, err
	}
	service, exists := engine.catalog.Object(preview.Service)
	if !exists {
		return model.Task{}, false, errors.New("受管对象声明不存在")
	}
	if err := engine.authorize(ctx, actorHash, permissionForAction(preview.Action), service.ObjectID); err != nil {
		return model.Task{}, false, err
	}
	taskID, err := newUUID()
	if err != nil {
		return model.Task{}, false, err
	}
	task, created, err := engine.store.StartTask(ctx, actorHash, request, taskID)
	if err != nil {
		_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{
			ActorHash: actorHash, Event: "task.rejected", Resource: request.PreviewID,
			Outcome: "rejected", Detail: map[string]any{"reason": redactText(err.Error())},
		})
		return model.Task{}, false, err
	}
	if !created {
		return task, false, nil
	}
	engine.enqueue(task)
	return task, true, nil
}

func (engine *Engine) enqueue(task model.Task) {
	engine.event(context.Background(), model.Event{TaskID: task.ID, Level: "info", Phase: "queued", Message: "任务已进入执行队列"})
	_, _ = engine.store.AppendAudit(context.Background(), model.AuditEntry{
		ActorHash: task.ActorHash, Event: "task.accepted", Resource: task.ID,
		Outcome: "accepted", Detail: map[string]any{"service": task.Service, "action": task.Action, "target": task.Target},
	})
	if engine.remoteDispatch {
		if err := engine.dispatchRemote(context.Background(), task); err != nil {
			engine.completeTask(task, model.TaskFailedRecoverable, "远程任务分派失败", redactText(err.Error()),
				"remote_dispatch_failed", true, false, "")
		}
		return
	}
	engine.wait.Add(1)
	go func() {
		defer engine.wait.Done()
		engine.run(task)
	}()
}

func (engine *Engine) dispatchRemote(ctx context.Context, task model.Task) error {
	service, action, err := engine.resolveAction(task.Service, task.Action, task.Target)
	if err != nil {
		return err
	}
	if service.ServerID == "" {
		return errors.New("远程任务目标未绑定 Fleet server")
	}
	runnerID := engine.runnerIDForService(ctx, service)
	if runnerID == "" {
		return errors.New("远程任务目标没有在线 Runner")
	}
	timeout := time.Duration(action.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	_, err = engine.store.CreateTaskAssignment(ctx, task.ID, service.ServerID, runnerID, time.Now().UTC().Add(timeout))
	return err
}

func (engine *Engine) Wait() {
	engine.wait.Wait()
}

func (engine *Engine) inspect(ctx context.Context, service model.ServiceDefinition) (map[string]any, error) {
	directory, err := os.MkdirTemp(engine.stateRoot, ".preview-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	inspectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := engine.executor.Execute(inspectCtx, ExecuteInput{
		Service: service, Action: "inspect", Phase: "inspect", OperationDir: directory,
		AdapterKind: adapterKindService,
	})
	if err != nil {
		return nil, err
	}
	data := cloneAnyMap(result.Data)
	if data == nil {
		data = make(map[string]any)
	}
	data["application"] = cloneAnyMap(result.Data)
	if service.TrafficPolicy != nil {
		traffic, trafficErr := engine.executor.Execute(inspectCtx, ExecuteInput{
			Service: service, Action: "inspect", Phase: "inspect", OperationDir: directory,
			AdapterKind: adapterKindTraffic,
		})
		if trafficErr != nil {
			return nil, fmt.Errorf("流量状态检查失败: %w", trafficErr)
		}
		data["traffic"] = cloneAnyMap(traffic.Data)
		for key, value := range traffic.Data {
			if key == "trafficState" || key == "includeDigest" || key == "hostname" || key == "drainTimeoutSeconds" {
				data[key] = value
			}
		}
	}
	return data, nil
}

func (engine *Engine) resolveAction(
	serviceName, actionName, target string,
) (model.ServiceDefinition, model.ActionDefinition, error) {
	service, ok := engine.catalog.Object(serviceName)
	if !ok {
		return service, model.ActionDefinition{}, errors.New("受管对象未纳入控制面")
	}
	action, ok := service.Actions[actionName]
	if !ok {
		if generated, generatedOK := engine.lifecycleAction(service, actionName); generatedOK {
			action, ok = generated, true
		}
	}
	if !ok || !action.Enabled {
		return service, action, errors.New("服务能力未开放")
	}
	switch action.TargetMode {
	case "none":
		isRecoveryPointTarget := (actionName == "restore" || actionName == "restore-drill") && uuidPattern.MatchString(target)
		if target != "" && !isRecoveryPointTarget {
			return service, action, errors.New("该动作不接受目标参数")
		}
	case "signed_release_tag":
		if !releasePattern.MatchString(target) {
			return service, action, errors.New("发布版本格式无效")
		}
	case "allowlist":
		if !contains(action.AllowedTargets, target) {
			return service, action, errors.New("发布目标尚未通过准备门禁")
		}
	case "controlled_rollback":
		if !uuidPattern.MatchString(target) {
			return service, action, errors.New("回滚来源任务无效")
		}
	default:
		return service, action, errors.New("服务目标策略无效")
	}
	return service, action, nil
}

func renderConfirmation(template, service, target string) string {
	result := strings.ReplaceAll(template, "{service}", service)
	return strings.ReplaceAll(result, "{target}", target)
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
