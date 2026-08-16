package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	catalog      *config.Catalog
	store        *store.Store
	executor     Executor
	broker       *Broker
	stateRoot    string
	lockMu       sync.Mutex
	locks        map[string]string
	credentialMu sync.Mutex
	wait         sync.WaitGroup
	owner        string
	backupRoot   string
	alertmanager Alertmanager
	credentials  CredentialRotator
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

func NewEngine(
	catalog *config.Catalog,
	database *store.Store,
	executor Executor,
	stateRoot string,
	options ...EngineOption,
) *Engine {
	owner, err := newUUID()
	if err != nil {
		owner = fmt.Sprintf("runner-%d", os.Getpid())
	}
	engine := &Engine{
		catalog: catalog, store: database, executor: executor, broker: NewBroker(),
		stateRoot: stateRoot, locks: make(map[string]string), owner: owner,
		backupRoot: "/var/backups/ops", alertmanager: unavailableAlertmanager{},
		credentials: unavailableCredentialRotator{},
	}
	for _, option := range options {
		option(engine)
	}
	return engine
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
	if active, found, err := engine.store.ActiveTask(ctx, service.Name); err != nil {
		return model.Preview{}, err
	} else if found {
		return model.Preview{}, fmt.Errorf("服务已有活动任务: %s", active.ID)
	}
	snapshot, err := engine.inspect(ctx, service)
	if err != nil {
		return model.Preview{}, fmt.Errorf("创建预览前检查失败: %w", err)
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
	engine.wait.Add(1)
	go func() {
		defer engine.wait.Done()
		engine.run(task)
	}()
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
	})
	if err != nil {
		return nil, err
	}
	return result.Data, nil
}

func (engine *Engine) resolveAction(
	serviceName, actionName, target string,
) (model.ServiceDefinition, model.ActionDefinition, error) {
	service, ok := engine.catalog.Object(serviceName)
	if !ok {
		return service, model.ActionDefinition{}, errors.New("受管对象未纳入控制面")
	}
	action, ok := service.Actions[actionName]
	if !ok || !action.Enabled {
		return service, action, errors.New("服务能力未开放")
	}
	switch action.TargetMode {
	case "none":
		if target != "" {
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
