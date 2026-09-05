package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

const (
	batchCoordinatorLeaseTTL      = 15 * time.Second
	batchCoordinatorRenewInterval = 5 * time.Second
	batchTaskPollInterval         = 250 * time.Millisecond
)

type batchCoordinatorLease struct {
	jobID string
	owner string
	token string
	lost  atomic.Bool
	done  chan struct{}
}

func (lease *batchCoordinatorLease) fence() store.BatchCoordinatorFence {
	return store.BatchCoordinatorFence{Owner: lease.owner, Token: lease.token}
}

func (engine *Engine) CreateBatch(ctx context.Context, actor string, request model.BatchCreateRequest) (model.BatchOperation, bool, error) {
	if !actorPattern.MatchString(actor) {
		return model.BatchOperation{}, false, errors.New("操作者标识无效")
	}
	if request.IdempotencyKey == "" || !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.BatchOperation{}, false, errors.New("批量请求幂等键无效")
	}
	if request.Action == "" {
		return model.BatchOperation{}, false, errors.New("批量动作不能为空")
	}
	if err := request.BatchPolicy.Validate(); err != nil {
		return model.BatchOperation{}, false, err
	}
	if err := request.Concurrency.Validate(); err != nil {
		return model.BatchOperation{}, false, err
	}
	if err := request.FailurePolicy.Validate(); err != nil {
		return model.BatchOperation{}, false, err
	}
	if request.ChangeWindow != nil {
		if err := request.ChangeWindow.Validate(); err != nil {
			return model.BatchOperation{}, false, err
		}
	}
	if request.FailurePolicy == model.FailureRollback {
		return model.BatchOperation{}, false, errors.New("批量自动回滚需要逐项目恢复点，当前版本要求人工执行")
	}
	if err := request.TargetSelector.Validate(); err != nil {
		return model.BatchOperation{}, false, err
	}
	if engine.catalog.SchemaVersion >= 4 && len(request.TargetIDs) == 0 {
		return model.BatchOperation{}, false, errors.New("生产批量作业必须显式列出目标 ID，禁止仅使用通配 selector")
	}
	if len(request.TargetIDs) == 0 && selectorOnlyExcludes(request.TargetSelector) {
		return model.BatchOperation{}, false, errors.New("批量 selector 必须包含明确的目标 ID、标签或能力")
	}
	targets, err := engine.resolveBatchTargets(ctx, request.TargetIDs, request.TargetSelector)
	if err != nil {
		return model.BatchOperation{}, false, err
	}
	if len(targets) == 0 {
		return model.BatchOperation{}, false, errors.New("批量 selector 没有匹配的目标服务")
	}
	if err := validateBatchConcurrencyTargets(request.Concurrency, targets, engine); err != nil {
		return model.BatchOperation{}, false, err
	}
	if err := engine.ensureBatchTargetsAvailable(ctx, targets); err != nil {
		return model.BatchOperation{}, false, err
	}
	if err := validateProductionBatchContract(engine.catalog.SchemaVersion, targets, request); err != nil {
		return model.BatchOperation{}, false, err
	}
	sort.Strings(targets)
	firstTenant := ""
	requiresDualApproval := false
	for _, target := range targets {
		service, exists := engine.catalog.Services[target]
		if !exists {
			return model.BatchOperation{}, false, fmt.Errorf("批量目标服务未登记: %s", target)
		}
		_, action, err := engine.resolveAction(target, request.Action, request.Target)
		if err != nil {
			return model.BatchOperation{}, false, err
		}
		if err := engine.authorize(ctx, actor, model.PermissionBatch, service.ObjectID); err != nil {
			return model.BatchOperation{}, false, err
		}
		if err := engine.authorize(ctx, actor, permissionForAction(action.Name), service.ObjectID); err != nil {
			return model.BatchOperation{}, false, err
		}
		if engine.catalog.SchemaVersion >= 4 && action.Risk == model.RiskHigh {
			requiresDualApproval = true
		}
		tenantID := service.TenantID
		if tenantID == "" && engine.catalog.Access != nil {
			tenantID = engine.catalog.Access.DefaultTenant
		}
		if tenantID == "" {
			tenantID = "default"
		}
		if firstTenant == "" {
			firstTenant = tenantID
		} else if firstTenant != tenantID {
			return model.BatchOperation{}, false, errors.New("批量目标必须属于同一租户")
		}
	}
	batches, err := request.BatchPolicy.Partition(targets)
	if err != nil {
		return model.BatchOperation{}, false, err
	}
	items := make([]model.BatchItem, 0, len(targets))
	itemIDs := make(map[string]string, len(targets))
	for index, serviceName := range targets {
		itemIDs[serviceName] = fmt.Sprintf("item-%03d-%s", index+1, serviceName)
	}
	for _, serviceName := range targets {
		batchIndex := batchIndexOf(batches, serviceName)
		dependencies := []string{}
		if batchIndex > 0 {
			for _, dependency := range batches[batchIndex-1] {
				dependencies = append(dependencies, itemIDs[dependency])
			}
		}
		service, exists := engine.catalog.Services[serviceName]
		if !exists {
			return model.BatchOperation{}, false, fmt.Errorf("批量目标服务未登记: %s", serviceName)
		}
		items = append(items, model.BatchItem{
			ID: itemIDs[serviceName], ObjectID: service.ObjectID, Service: serviceName,
			ServerID: service.ServerID, RunnerID: engine.runnerIDForService(ctx, service),
			BatchIndex: batchIndex, DependsOn: dependencies, State: model.BatchNodePending,
			UpdatedAt: time.Now().UTC(),
		})
	}
	nodes := make([]model.DAGNode, 0, len(items))
	for _, item := range items {
		nodes = append(nodes, model.DAGNode{ID: item.ID, Action: request.Action, TargetID: item.Service, Dependencies: item.DependsOn, State: model.BatchNodePending})
	}
	now := time.Now().UTC()
	task := model.BatchTask{ID: "batch-" + request.IdempotencyKey[:8], Action: request.Action,
		TargetSelector: request.TargetSelector, TargetIDs: targets, Nodes: nodes,
		BatchPolicy: request.BatchPolicy, Concurrency: request.Concurrency,
		FailurePolicy: request.FailurePolicy, ChangeWindow: request.ChangeWindow,
		State: model.BatchTaskPending, CreatedAt: now}
	// The digest identifies the requested rollout, not when the plan happened
	// to be assembled.  CreatedAt/UpdatedAt are intentionally omitted from the
	// canonical copy so replaying the same idempotency key returns the original
	// operation instead of looking like a conflicting request.
	digestTask := task
	digestTask.CreatedAt = time.Time{}
	digestItems := make([]model.BatchItem, len(items))
	copy(digestItems, items)
	for index := range digestItems {
		digestItems[index].UpdatedAt = time.Time{}
	}
	payload, _ := json.Marshal(struct {
		Action string            `json:"action"`
		Target string            `json:"target"`
		Task   model.BatchTask   `json:"task"`
		Items  []model.BatchItem `json:"items"`
	}{request.Action, request.Target, digestTask, digestItems})
	digestBytes := sha256.Sum256(payload)
	digest := hex.EncodeToString(digestBytes[:])
	if firstTenant == "" {
		firstTenant = "default"
	}
	op := model.BatchOperation{ID: task.ID, IdempotencyKey: request.IdempotencyKey, ActorHash: actor, TenantID: firstTenant, Action: request.Action, Target: request.Target, Task: task, Digest: digest, ConfirmationPhrase: fmt.Sprintf("批量%s %d 项", lifecycleDisplayName(request.Action), len(targets)), State: model.BatchPendingApproval, RequiresDualApproval: requiresDualApproval, ApprovalPolicyVersion: model.CurrentBatchApprovalPolicyVersion, Items: items, CreatedAt: now, UpdatedAt: now}
	created, wasCreated, err := engine.store.CreateBatchOperation(ctx, store.BatchOperationInput{Operation: op, ConfirmationHash: store.HashConfirmation(op.ConfirmationPhrase)})
	if err != nil {
		return model.BatchOperation{}, false, err
	}
	if !wasCreated {
		// CreateBatchOperation intentionally returns a lightweight idempotent
		// lookup. Hydrate items before handing the operation to callers so a
		// replay has the same approval/inspection surface as the first request.
		created, err = engine.store.GetBatchOperation(ctx, created.ID)
		if err != nil {
			return model.BatchOperation{}, false, err
		}
	}
	if created.ConfirmationPhrase == "" {
		// The durable schema stores only the confirmation hash. The phrase is a
		// deterministic rendering of this request, so it can be reconstructed
		// without persisting sensitive or redundant plaintext.
		created.ConfirmationPhrase = op.ConfirmationPhrase
	}
	return created, wasCreated, nil
}

// validateProductionBatchContract turns the rollout safety rules into a
// creation-time invariant.  A persisted production batch must never be able to
// bypass an explicit target list, a canary observation gate, or the
// fail-stop policy merely because a caller selected a different strategy.
// Schema 3 remains available for legacy/local fixtures; schema 4 is the
// production-equivalent contract.
func validateProductionBatchContract(schemaVersion int, targets []string, request model.BatchCreateRequest) error {
	if schemaVersion < 4 {
		return nil
	}
	if len(request.TargetIDs) == 0 {
		return errors.New("生产批量作业必须显式列出目标 ID，禁止仅使用通配 selector")
	}
	if len(targets) <= 1 {
		if request.FailurePolicy == model.FailureContinue {
			return errors.New("生产批量作业禁止 failurePolicy=continue")
		}
		return nil
	}
	if request.BatchPolicy.Strategy != model.BatchCanary {
		return errors.New("生产多目标批量作业必须先执行 Canary")
	}
	if request.BatchPolicy.ObservationSeconds <= 0 {
		return errors.New("生产 Canary 必须配置正数观察窗口")
	}
	if request.FailurePolicy != model.FailureStop {
		return errors.New("生产批量作业失败后必须停止后续批次")
	}
	return nil
}

func selectorOnlyExcludes(selector model.NodeSelector) bool {
	return len(selector.IDs) == 0 && len(selector.MatchLabels) == 0 &&
		len(selector.MatchCapabilities) == 0 && len(selector.ExcludeIDs) > 0
}

func selectorHasCriteria(selector model.NodeSelector) bool {
	return len(selector.IDs) > 0 || len(selector.MatchLabels) > 0 ||
		len(selector.MatchCapabilities) > 0 || len(selector.ExcludeIDs) > 0
}

func validateBatchConcurrencyTargets(policy model.ConcurrencyPolicy, targets []string, engine *Engine) error {
	if policy.Scope == model.ConcurrencyGlobal {
		return nil
	}
	for _, target := range targets {
		service, ok := engine.catalog.Services[target]
		if !ok {
			return fmt.Errorf("批量目标服务未登记: %s", target)
		}
		key := service.ServerID
		if policy.Scope == model.ConcurrencyPerRunner {
			key = engine.runnerIDForService(context.Background(), service)
			if key == "" {
				return fmt.Errorf("批量目标 %s 未绑定可用 Runner，无法应用 per_runner 并发限制", target)
			}
		}
		if key == "" {
			return fmt.Errorf("批量目标 %s 未绑定 Fleet 节点，无法应用并发限制", target)
		}
	}
	return nil
}

// resolveBatchTargets turns either explicit service IDs or a fleet selector
// into the immutable service list stored in the batch digest. Selector matching
// accepts service IDs as well as their registered server/Runner IDs, allowing
// the API's fleet vocabulary to remain useful for service operations.
func (engine *Engine) resolveBatchTargets(
	ctx context.Context,
	explicit []string,
	selector model.NodeSelector,
) ([]string, error) {
	if err := validateBatchTargets(explicit); err != nil {
		return nil, err
	}
	if !selectorHasCriteria(selector) {
		return engine.expandExplicitBatchTargets(explicit)
	}
	candidates, err := engine.expandExplicitBatchTargets(explicit)
	if err != nil {
		return nil, err
	}
	if len(explicit) == 0 {
		candidates = make([]string, 0, len(engine.catalog.Services))
		for name := range engine.catalog.Services {
			candidates = append(candidates, name)
		}
		sort.Strings(candidates)
	}
	servers, runners := engine.selectorFleet(ctx)
	selected := make([]string, 0, len(candidates))
	for _, name := range candidates {
		service, exists := engine.catalog.Services[name]
		if !exists {
			continue
		}
		if serviceMatchesSelector(service, selector, servers, runners) {
			selected = append(selected, name)
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("批量 selector 没有匹配的目标服务")
	}
	return selected, nil
}

func (engine *Engine) expandExplicitBatchTargets(targets []string) ([]string, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	result := make([]string, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if _, exists := engine.catalog.Services[target]; exists {
			if _, duplicate := seen[target]; !duplicate {
				result = append(result, target)
				seen[target] = struct{}{}
			}
			continue
		}
		matched := false
		for name, service := range engine.catalog.Services {
			if service.ServerID != target {
				continue
			}
			matched = true
			if _, duplicate := seen[name]; !duplicate {
				result = append(result, name)
				seen[name] = struct{}{}
			}
		}
		if !matched {
			for name, service := range engine.catalog.Services {
				if server := engine.catalogServer(service.ServerID); server.RunnerID != target {
					continue
				}
				if service.ServerID == "" {
					continue
				}
				matched = true
				if _, duplicate := seen[name]; !duplicate {
					result = append(result, name)
					seen[name] = struct{}{}
				}
			}
		}
		if !matched {
			return nil, fmt.Errorf("批量目标服务或 Fleet 节点未登记: %s", target)
		}
	}
	sort.Strings(result)
	return result, nil
}

func (engine *Engine) selectorFleet(ctx context.Context) (map[string]model.ServerNode, map[string]model.RunnerNode) {
	servers := make(map[string]model.ServerNode)
	runners := make(map[string]model.RunnerNode)
	if engine.catalog.Fleet != nil {
		for _, server := range engine.catalog.Fleet.Inventory.Servers {
			servers[server.ID] = server
		}
		for _, runner := range engine.catalog.Fleet.Inventory.Runners {
			runners[runner.ID] = runner
		}
	}
	// Heartbeats may update labels/capabilities after bootstrap. The durable
	// registry is authoritative when it is available, but a legacy catalog with
	// no Fleet tables remains valid for explicit service batches.
	if fleet, err := engine.store.ListFleet(ctx); err == nil {
		for _, server := range fleet.Servers {
			servers[server.ID] = server
		}
		for _, runner := range fleet.Runners {
			runners[runner.ID] = runner
		}
	}
	return servers, runners
}

// ensureBatchTargetsAvailable is intentionally a second gate after selector
// resolution. Selectors are expanded into an immutable target list at plan
// creation, but a Runner lease can expire before approval or execution. Legacy
// catalogs without Fleet remain local-runner compatible; an enabled Fleet must
// prove a live Runner for every target before a batch can mutate anything.
func (engine *Engine) ensureBatchTargetsAvailable(ctx context.Context, targets []string) error {
	if engine.catalog.Fleet == nil || !engine.catalog.Fleet.Enabled {
		return nil
	}
	timeout := time.Duration(engine.catalog.Fleet.HeartbeatTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	servers, runners := engine.selectorFleet(ctx)
	now := time.Now().UTC()
	for _, name := range targets {
		service, exists := engine.catalog.Services[name]
		if !exists {
			return fmt.Errorf("批量目标服务未登记: %s", name)
		}
		if service.ServerID == "" {
			return fmt.Errorf("批量目标 %s 未绑定 Fleet server", name)
		}
		server, exists := servers[service.ServerID]
		if !exists {
			return fmt.Errorf("批量目标 %s 的 Fleet server 未登记: %s", name, service.ServerID)
		}
		if server.State != model.NodeOnline {
			return fmt.Errorf("批量目标 %s 的 Fleet server 当前不可用: %s", name, server.State)
		}
		available := false
		for _, runner := range runnersForServer(server.ID, server.RunnerID, runners) {
			if runner.AvailableAt(now, timeout) {
				available = true
				break
			}
		}
		if !available {
			return fmt.Errorf("批量目标 %s 的 Runner 不在线或心跳租约已过期", name)
		}
	}
	return nil
}

// ensureServiceTargetAvailable applies the same Fleet gate to a single
// release-plan execution.  It is kept in batch.go so batch and single-service
// mutations cannot drift into different heartbeat/lease interpretations.
func (engine *Engine) ensureServiceTargetAvailable(ctx context.Context, serviceName string) error {
	if serviceName == "" {
		return errors.New("服务目标不能为空")
	}
	return engine.ensureBatchTargetsAvailable(ctx, []string{serviceName})
}

func (engine *Engine) catalogServer(id string) model.ServerNode {
	if engine.catalog.Fleet == nil {
		return model.ServerNode{}
	}
	for _, server := range engine.catalog.Fleet.Inventory.Servers {
		if server.ID == id {
			return server
		}
	}
	return model.ServerNode{}
}

func serviceMatchesSelector(
	service model.ServiceDefinition,
	selector model.NodeSelector,
	servers map[string]model.ServerNode,
	runners map[string]model.RunnerNode,
) bool {
	serviceLabels := serviceSelectorLabels(service)
	serviceCapabilities := append([]string(nil), service.Capabilities...)
	server, serverOK := servers[service.ServerID]
	associatedRunners := runnersForServer(service.ServerID, server.RunnerID, runners)
	if contains(selector.ExcludeIDs, service.Name) ||
		(service.ServerID != "" && contains(selector.ExcludeIDs, service.ServerID)) ||
		containsAnyRunner(selector.ExcludeIDs, associatedRunners) {
		return false
	}
	aggregateLabels := serviceLabels
	aggregateCapabilities := serviceCapabilities
	if serverOK {
		aggregateLabels = mergeLabels(server.Labels, aggregateLabels)
		aggregateCapabilities = mergeCapabilities(server.Capabilities, aggregateCapabilities)
	}
	// Test the service identity with server metadata merged in. This supports a
	// selector that names a service but constrains a fleet label/capability.
	if selector.MatchesServer(model.ServerNode{ID: service.Name, Labels: aggregateLabels, Capabilities: aggregateCapabilities}) {
		return true
	}
	if service.ServerID == "" {
		return false
	}
	if selector.MatchesServer(model.ServerNode{ID: service.ServerID,
		Labels: aggregateLabels, Capabilities: aggregateCapabilities}) {
		return true
	}
	for _, runner := range associatedRunners {
		runnerLabels := mergeLabels(runner.Labels, aggregateLabels)
		runnerCapabilities := mergeCapabilities(runner.Capabilities, aggregateCapabilities)
		if selector.MatchesRunner(model.RunnerNode{ID: runner.ID, ServerID: runner.ServerID,
			Labels: runnerLabels, Capabilities: runnerCapabilities}) {
			return true
		}
	}
	// A service may reference a server before Fleet bootstrap is present. Keep
	// ID matching deterministic while still applying service metadata filters.
	return !serverOK && selector.MatchesServer(model.ServerNode{ID: service.ServerID,
		Labels: serviceLabels, Capabilities: serviceCapabilities})
}

// runnersForServer resolves both the explicit server.runnerId association and
// the inverse runner.serverId reference. Older bootstrap catalogs may leave
// the server-side field empty, so selector matching must support both forms.
// Sorting keeps expansion deterministic even though the source is a map.
func runnersForServer(serverID, preferredID string, runners map[string]model.RunnerNode) []model.RunnerNode {
	if serverID == "" {
		return nil
	}
	result := make([]model.RunnerNode, 0, len(runners))
	if preferredID != "" {
		if preferred, ok := runners[preferredID]; ok && preferred.ServerID == serverID {
			result = append(result, preferred)
		}
	}
	for id, runner := range runners {
		if runner.ServerID != serverID || runner.ID == preferredID {
			continue
		}
		if runner.ID == "" {
			runner.ID = id
		}
		result = append(result, runner)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}

func containsAnyRunner(ids []string, runners []model.RunnerNode) bool {
	for _, runner := range runners {
		if contains(ids, runner.ID) {
			return true
		}
	}
	return false
}

func serviceSelectorLabels(service model.ServiceDefinition) map[string]string {
	labels := map[string]string{"service": service.Name, "objectId": service.ObjectID}
	metadata := service.Metadata
	if metadata.Type != "" {
		labels["type"] = metadata.Type
	}
	if metadata.Environment != "" {
		labels["environment"] = metadata.Environment
	}
	if metadata.Lifecycle != "" {
		labels["lifecycle"] = metadata.Lifecycle
	}
	if metadata.Criticality != "" {
		labels["criticality"] = metadata.Criticality
	}
	if metadata.Maturity != "" {
		labels["maturity"] = metadata.Maturity
	}
	if service.TenantID != "" {
		labels["tenant"] = service.TenantID
	}
	if service.ServerID != "" {
		labels["server"] = service.ServerID
	}
	return labels
}

func mergeLabels(primary, secondary map[string]string) map[string]string {
	result := make(map[string]string, len(primary)+len(secondary))
	for key, value := range primary {
		result[key] = value
	}
	for key, value := range secondary {
		if _, exists := result[key]; !exists {
			result[key] = value
		}
	}
	return result
}

func mergeCapabilities(groups ...[]string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, group := range groups {
		for _, capability := range group {
			if _, exists := seen[capability]; exists {
				continue
			}
			seen[capability] = struct{}{}
			result = append(result, capability)
		}
	}
	return result
}

func validateBatchTargets(targets []string) error {
	seen := map[string]bool{}
	for _, target := range targets {
		if !uuidOrServiceName(target) {
			return fmt.Errorf("批量目标无效: %s", target)
		}
		if seen[target] {
			return fmt.Errorf("批量目标重复: %s", target)
		}
		seen[target] = true
	}
	return nil
}

func uuidOrServiceName(value string) bool {
	return value != "" && (releasePattern.MatchString(value) == false)
}

func batchIndexOf(batches [][]string, target string) int {
	for index, batch := range batches {
		for _, candidate := range batch {
			if candidate == target {
				return index
			}
		}
	}
	return 0
}
func batchItemIDs(targets []string) []string {
	result := make([]string, 0, len(targets))
	for index, target := range targets {
		result = append(result, fmt.Sprintf("item-%03d-%s", index+1, target))
	}
	return result
}

func (engine *Engine) BatchOperation(ctx context.Context, id string) (model.BatchOperation, error) {
	return engine.store.GetBatchOperation(ctx, id)
}
func (engine *Engine) BatchOperations(ctx context.Context, limit, offset int) ([]model.BatchOperation, error) {
	return engine.store.ListBatchOperations(ctx, limit, offset)
}

func (engine *Engine) BatchOperationsForActor(
	ctx context.Context,
	actor string,
	limit, offset int,
) ([]model.BatchOperation, bool, error) {
	if err := engine.validateActorTenant(ctx, actor); err != nil {
		return nil, false, err
	}
	all := make([]model.BatchOperation, 0)
	for pageOffset := 0; ; pageOffset += collectionScanPageSize {
		page, err := engine.store.ListBatchOperations(ctx, collectionScanPageSize, pageOffset)
		if err != nil {
			return nil, false, err
		}
		for _, operation := range page {
			if err := engine.authorizeBatchRead(ctx, actor, operation); err == nil {
				all = append(all, operation)
			} else if !isAuthorizationError(err) {
				return nil, false, err
			}
		}
		if len(page) < collectionScanPageSize {
			break
		}
	}
	return paginateCollection(all, limit, offset)
}

// authorizeBatchRead applies the read boundary to every item. A batch is a
// set of objects, so authorizing only its stored TenantID or job id would
// allow a mixed/forged operation to disclose another tenant's service names.
func (engine *Engine) authorizeBatchRead(
	ctx context.Context,
	actor string,
	operation model.BatchOperation,
) error {
	if len(operation.Items) == 0 {
		return authorizationError{message: "批量作业没有可授权项目"}
	}
	for _, item := range operation.Items {
		object, exists := engine.catalog.Object(item.Service)
		if !exists || (item.ObjectID != "" && object.ObjectID != item.ObjectID) {
			return authorizationError{message: "批量项目所属对象未登记"}
		}
		if operation.TenantID != "" {
			tenantID := object.TenantID
			if tenantID == "" && engine.catalog.Access != nil {
				tenantID = engine.catalog.Access.DefaultTenant
			}
			if tenantID != "" && tenantID != operation.TenantID {
				return authorizationError{message: "批量作业租户与目标对象不一致"}
			}
		}
		if err := engine.authorize(ctx, actor, model.PermissionRead, object.ObjectID); err != nil {
			return err
		}
	}
	return nil
}

func (engine *Engine) ApproveBatch(ctx context.Context, actor, id string, input model.BatchApproveRequest) (model.BatchOperation, error) {
	op, err := engine.store.GetBatchOperation(ctx, id)
	if err != nil {
		return model.BatchOperation{}, err
	}
	if err := engine.authorizeBatchOperation(ctx, actor, op); err != nil {
		return model.BatchOperation{}, err
	}
	if engine.catalog.SchemaVersion >= 4 && op.ActorHash == actor {
		return model.BatchOperation{}, store.ErrActorMismatch
	}
	approved, err := engine.store.ApproveBatchOperation(ctx, id, actor, input.Digest, input.Confirmation)
	if err != nil {
		return model.BatchOperation{}, err
	}
	return approved, nil
}

func (engine *Engine) ExecuteBatch(ctx context.Context, actor, id string, input model.BatchExecuteRequest) (model.BatchOperation, error) {
	op, err := engine.store.GetBatchOperation(ctx, id)
	if err != nil {
		return model.BatchOperation{}, err
	}
	if err := engine.authorizeBatchOperation(ctx, actor, op); err != nil {
		return model.BatchOperation{}, err
	}
	if op.RequiresDualApproval && op.SecondApprovedByHash == "" {
		return model.BatchOperation{}, errors.New("生产批量计划尚未完成独立双人批准")
	}
	if engine.catalog.SchemaVersion >= 4 && (op.ActorHash == actor ||
		(op.ApprovedByHash != "" && op.ApprovedByHash == actor) ||
		(op.SecondApprovedByHash != "" && op.SecondApprovedByHash == actor)) {
		return model.BatchOperation{}, store.ErrActorMismatch
	}
	if !uuidPattern.MatchString(input.IdempotencyKey) {
		return model.BatchOperation{}, errors.New("批量执行幂等键无效")
	}
	// A plan may be replayed after it has already started; in that case the
	// durable idempotency key is authoritative and no second heartbeat gate is
	// needed. A fresh start must re-check the lease because approval can outlive
	// the Runner heartbeat window.
	if op.State == model.BatchApproved {
		if !batchWindowOpen(op, time.Now().UTC()) {
			return model.BatchOperation{}, errors.New("批量变更窗口当前未开放")
		}
		if err := engine.ensureBatchTargetsAvailable(ctx, op.Task.TargetIDs); err != nil {
			return model.BatchOperation{}, err
		}
	}
	op, started, err := engine.store.StartBatchOperation(ctx, id, actor, input.IdempotencyKey)
	if err != nil {
		return model.BatchOperation{}, err
	}
	if !started || op.State != model.BatchRunning {
		return op, nil
	}
	engine.wait.Add(1)
	go func() { defer engine.wait.Done(); engine.runBatch(op) }()
	return op, nil
}

func (engine *Engine) runBatch(op model.BatchOperation) {
	ctx := context.Background()
	token, _, acquired, err := engine.store.AcquireBatchCoordinator(ctx, op.ID, engine.owner, batchCoordinatorLeaseTTL)
	if err != nil {
		return
	}
	if !acquired {
		fence, _, _, lookupErr := engine.store.GetBatchCoordinator(ctx, op.ID)
		if lookupErr != nil || fence.Owner != engine.owner {
			return
		}
		token = fence.Token
		ok, renewErr := engine.store.RenewBatchCoordinator(ctx, op.ID, engine.owner, token, batchCoordinatorLeaseTTL)
		if renewErr != nil || !ok {
			return
		}
	}
	if token == "" {
		return
	}
	lease := &batchCoordinatorLease{jobID: op.ID, owner: engine.owner, token: token, done: make(chan struct{})}
	go engine.renewBatchCoordinator(lease)
	defer func() {
		close(lease.done)
		_ = engine.store.ReleaseBatchCoordinator(context.Background(), lease.jobID, lease.owner, lease.token)
	}()
	fence := lease.fence()
	for {
		if lease.lost.Load() {
			return
		}
		current, err := engine.store.GetBatchOperation(ctx, op.ID)
		if err != nil {
			return
		}
		if current.ApprovalPolicyVersion != model.CurrentBatchApprovalPolicyVersion {
			_ = engine.store.FinishBatchOperation(ctx, current.ID, model.BatchNeedsAttention,
				"批量计划审批策略版本过旧", "请按当前审批策略重新创建批量计划", fence)
			return
		}
		if current.RequiresDualApproval && !model.IndependentExecutor(current.ExecutedByHash,
			current.ActorHash, current.ApprovedByHash, current.SecondApprovedByHash) {
			_ = engine.store.FinishBatchOperation(ctx, current.ID, model.BatchNeedsAttention,
				"批量计划四方身份不完整", "请重新创建并完成独立双人批准与独立执行", fence)
			return
		}
		pending, running, terminalFailure := batchWave(current)
		if current.Task.BatchPolicy.Strategy == model.BatchCanary && canaryWaveFailed(current) {
			_ = engine.store.FinishBatchOperation(ctx, current.ID, model.BatchPaused,
				"canary 执行或观察失败，批量作业暂停", "请核对 canary 项目后人工继续", fence)
			return
		}
		if terminalFailure && current.Task.FailurePolicy != model.FailureContinue {
			_ = engine.store.FinishBatchOperation(ctx, current.ID, model.BatchNeedsAttention, "批量作业存在失败项目", "请核对失败项目后人工处理", fence)
			return
		}
		if len(running) > 0 {
			if current.Task.BatchPolicy.Strategy == model.BatchCanary && current.State == model.BatchObserving {
				if !engine.waitBatchCanaryObservation(ctx, current, running, lease) {
					return
				}
				continue
			}
			if !engine.waitBatchTasks(ctx, current, running, lease) {
				return
			}
			continue
		}
		if len(pending) == 0 {
			if terminalFailure {
				_ = engine.store.FinishBatchOperation(ctx, current.ID, model.BatchNeedsAttention, "批量作业完成但存在失败项目", "请核对失败项目", fence)
			} else {
				_ = engine.store.FinishBatchOperation(ctx, current.ID, model.BatchSucceeded, "所有批量项目已完成", "", fence)
			}
			return
		}
		if current.State == model.BatchObserving {
			if !engine.waitBatchCanaryObservation(ctx, current, nil, lease) {
				return
			}
			continue
		}
		if current.Task.BatchPolicy.Strategy == model.BatchCanary && current.CanaryObservedAt == nil && current.CanaryObservationStartedAt == nil && canaryWaveComplete(current) {
			if err := engine.store.BeginBatchCanaryObservation(ctx, current.ID, fence); err != nil {
				return
			}
			continue
		}
		// A running batch may outlive its approved change window. Only a new
		// wave is blocked by a closed window; an already completed batch must
		// still reach its terminal state and never become paused retroactively.
		if !batchWindowOpen(current, time.Now().UTC()) {
			_ = engine.store.FinishBatchOperation(ctx, current.ID, model.BatchPaused,
				"变更窗口已关闭，批量作业暂停", "请在新的变更窗口内重新核对并执行", fence)
			return
		}
		pending = engine.selectBatchItems(current, pending)
		if len(pending) == 0 {
			// A full per-runner/per-server slot is expected while active tasks
			// drain; avoid spinning the coordinator at 100% CPU.
			time.Sleep(batchTaskPollInterval)
			continue
		}
		for _, item := range pending {
			if lease.lost.Load() {
				return
			}
			if err := engine.store.UpdateBatchItemCAS(ctx, current.ID, item.ID, model.BatchNodePending, model.BatchNodeReady, "", "", "", fence); err != nil {
				_ = engine.store.FinishBatchOperation(ctx, current.ID, model.BatchNeedsAttention,
					"批量项目调度状态发生冲突", redactText(err.Error()), fence)
				return
			}
			engine.startBatchItem(ctx, current, item, lease)
		}
	}
}

func canaryWaveComplete(op model.BatchOperation) bool {
	found := false
	for _, item := range op.Items {
		if item.BatchIndex != 0 {
			continue
		}
		found = true
		if item.State != model.BatchNodeSucceeded {
			return false
		}
	}
	return found
}

func canaryWaveFailed(op model.BatchOperation) bool {
	for _, item := range op.Items {
		if item.BatchIndex == 0 && item.State.Terminal() && item.State != model.BatchNodeSucceeded {
			return true
		}
	}
	return false
}

func (engine *Engine) selectBatchItems(op model.BatchOperation, pending []model.BatchItem) []model.BatchItem {
	policy := op.Task.Concurrency
	global := policy.MaxConcurrent
	if global < 1 {
		global = 1
	}
	queue := len(pending)
	if policy.QueueLimit > 0 && queue > policy.QueueLimit {
		queue = policy.QueueLimit
	}
	if global < queue {
		queue = global
	}
	// QueueLimit bounds the number of not-yet-admitted items. It must not
	// reject a valid large plan at creation time; later waves remain durable.
	if policy.QueueLimit > 0 && queue > policy.QueueLimit {
		queue = policy.QueueLimit
	}
	selected := make([]model.BatchItem, 0, queue)
	runnerCounts := make(map[string]int)
	serverCounts := make(map[string]int)
	activeRunner, activeServer := engine.activeBatchCounts(op.Items)
	for _, item := range pending {
		if len(selected) >= queue {
			break
		}
		runnerLimit, serverLimit := policy.PerRunner, policy.PerServer
		runnerKey, serverKey := item.RunnerID, item.ServerID
		if policy.Scope == model.ConcurrencyPerRunner && runnerLimit > 0 && runnerCounts[runnerKey] >= runnerLimit {
			continue
		}
		if policy.Scope == model.ConcurrencyPerServer && serverLimit > 0 && serverCounts[serverKey] >= serverLimit {
			continue
		}
		// Count currently active items too, so a resumed coordinator cannot
		// exceed a limit while tasks from the previous process are still live.
		if policy.Scope == model.ConcurrencyPerRunner && runnerLimit > 0 && activeRunner[runnerKey]+runnerCounts[runnerKey] >= runnerLimit {
			continue
		}
		if policy.Scope == model.ConcurrencyPerServer && serverLimit > 0 && activeServer[serverKey]+serverCounts[serverKey] >= serverLimit {
			continue
		}
		selected = append(selected, item)
		runnerCounts[runnerKey]++
		serverCounts[serverKey]++
	}
	return selected
}

func (engine *Engine) activeBatchCounts(items []model.BatchItem) (map[string]int, map[string]int) {
	runners := make(map[string]int)
	servers := make(map[string]int)
	for _, item := range items {
		if item.State != model.BatchNodeReady && item.State != model.BatchNodeRunning {
			continue
		}
		if item.RunnerID != "" {
			runners[item.RunnerID]++
		}
		if item.ServerID != "" {
			servers[item.ServerID]++
		}
	}
	return runners, servers
}

func (engine *Engine) waitBatchCanaryObservation(ctx context.Context, op model.BatchOperation, items []model.BatchItem, lease *batchCoordinatorLease) bool {
	if len(items) > 0 {
		if !engine.waitBatchTasks(ctx, op, items, lease) {
			return false
		}
	}
	current, err := engine.store.GetBatchOperation(ctx, op.ID)
	if err != nil {
		return false
	}
	if current.State != model.BatchObserving {
		return true
	}
	if current.CanaryObservationStartedAt == nil {
		return false
	}
	for _, item := range current.Items {
		if item.BatchIndex == 0 && item.State != model.BatchNodeSucceeded {
			_ = engine.store.FinishBatchOperation(ctx, current.ID, model.BatchPaused, "canary 执行失败，批量作业暂停", "请核对 canary 项目后人工继续", lease.fence())
			return false
		}
	}
	duration := time.Duration(current.Task.BatchPolicy.ObservationSeconds) * time.Second
	if duration <= 0 {
		if err := engine.checkBatchCanaryHealth(ctx, current); err != nil {
			_ = engine.store.FinishBatchOperation(ctx, current.ID, model.BatchPaused, "canary 观察失败，批量作业暂停", redactText(err.Error()), lease.fence())
			return false
		}
		if err := engine.store.CompleteBatchCanaryObservation(ctx, current.ID, lease.fence()); err != nil {
			return false
		}
		return true
	}
	deadline := current.CanaryObservationStartedAt.Add(duration)
	if time.Now().UTC().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Until(deadline)):
		}
	}
	if lease.lost.Load() {
		return false
	}
	if err := engine.checkBatchCanaryHealth(ctx, current); err != nil {
		_ = engine.store.FinishBatchOperation(ctx, current.ID, model.BatchPaused, "canary 观察失败，批量作业暂停", redactText(err.Error()), lease.fence())
		return false
	}
	if err := engine.store.CompleteBatchCanaryObservation(ctx, current.ID, lease.fence()); err != nil {
		return false
	}
	return true
}

func (engine *Engine) checkBatchCanaryHealth(ctx context.Context, op model.BatchOperation) error {
	for _, item := range op.Items {
		if item.BatchIndex != 0 {
			continue
		}
		service, ok := engine.catalog.Services[item.Service]
		if !ok {
			return fmt.Errorf("canary 服务未登记: %s", item.Service)
		}
		blockers, err := engine.blockingAlerts(ctx, service)
		if err != nil {
			return fmt.Errorf("canary 观察无法读取 Alertmanager: %w", err)
		}
		if len(blockers) > 0 {
			return fmt.Errorf("canary 关联阻断告警仍在触发: %s", alertNames(blockers))
		}
		if _, err := engine.inspectForAction(ctx, service, op.Action); err != nil {
			return fmt.Errorf("canary 健康检查失败: %w", err)
		}
	}
	return nil
}

func (engine *Engine) renewBatchCoordinator(lease *batchCoordinatorLease) {
	ticker := time.NewTicker(batchCoordinatorRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-lease.done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			ok, err := engine.store.RenewBatchCoordinator(ctx, lease.jobID, lease.owner, lease.token, batchCoordinatorLeaseTTL)
			cancel()
			if err != nil || !ok {
				lease.lost.Store(true)
				return
			}
		}
	}
}

func (engine *Engine) resumeBatchOperations() error {
	ids, err := engine.store.ListActiveBatchOperations(context.Background())
	if err != nil {
		return err
	}
	for _, id := range ids {
		op, err := engine.store.GetBatchOperation(context.Background(), id)
		if err != nil {
			return err
		}
		switch op.State {
		case model.BatchRunning, model.BatchObserving:
			// Running and observing are coordinator-owned states. Re-enter the
			// durable wave loop; item/task CAS guards make this restart-safe.
			engine.wait.Add(1)
			go func(operation model.BatchOperation) {
				defer engine.wait.Done()
				engine.runBatch(operation)
			}(op)
		case model.BatchPaused:
			// A pause is an explicit operator decision. Never turn it into an
			// implicit rollout merely because the Runner restarted.
			continue
		case model.BatchRollingBack:
			// Automatic batch rollback is not supported by this Runner. A
			// restart while rollback is in progress must stop at a durable,
			// auditable manual boundary instead of resuming the forward wave.
			_ = engine.store.FinishBatchOperation(context.Background(), op.ID,
				model.BatchNeedsAttention, "Runner 重启时批量回滚未完成", "请人工核对每个项目的回滚任务和运行身份")
		}
	}
	return nil
}

func batchWave(op model.BatchOperation) (pending, running []model.BatchItem, failure bool) {
	minWave := -1
	for _, item := range op.Items {
		if item.State.Terminal() && item.State != model.BatchNodeSucceeded {
			failure = true
		}
		if item.State == model.BatchNodeRunning || item.State == model.BatchNodeReady {
			running = append(running, item)
		}
		if item.State == model.BatchNodePending && (minWave < 0 || item.BatchIndex < minWave) {
			minWave = item.BatchIndex
		}
	}
	if minWave >= 0 {
		for _, item := range op.Items {
			if item.State == model.BatchNodePending && item.BatchIndex == minWave {
				pending = append(pending, item)
			}
		}
	}
	return pending, running, failure
}

func (engine *Engine) startBatchItem(ctx context.Context, op model.BatchOperation, item model.BatchItem, lease ...*batchCoordinatorLease) {
	var fence []store.BatchCoordinatorFence
	if len(lease) > 0 && lease[0] != nil {
		fence = append(fence, lease[0].fence())
	}
	// A crash may have committed the plan/task before the item association.
	// Re-read the deterministic request keys and bind the durable task first.
	planKey := batchItemIdempotencyKey(op.ID, item.ID, "plan")
	taskKey := batchItemIdempotencyKey(op.ID, item.ID, "execute")
	if _, _, bindErr := engine.store.BindBatchItemExecution(ctx, op.ID, item.ID, planKey, taskKey, fence...); bindErr == nil {
		return
	}
	if op.ApprovalPolicyVersion != model.CurrentBatchApprovalPolicyVersion {
		_ = engine.failReadyBatchItem(ctx, op.ID, item.ID, "", errors.New("父批量计划审批策略版本过旧"), fence...)
		return
	}
	_, action, err := engine.resolveAction(item.Service, op.Action, op.Target)
	if err != nil {
		_ = engine.failReadyBatchItem(ctx, op.ID, item.ID, "", err, fence...)
		return
	}
	childRequiresDual := action.Risk == model.RiskHigh
	if childRequiresDual && !op.RequiresDualApproval {
		_ = engine.failReadyBatchItem(ctx, op.ID, item.ID, "", errors.New("高风险批量子计划缺少父级双人批准策略"), fence...)
		return
	}
	planCreator, planExecutor, err := batchChildActors(op, childRequiresDual)
	if err != nil {
		_ = engine.failReadyBatchItem(ctx, op.ID, item.ID, "", err, fence...)
		return
	}
	plan, err := engine.CreateReleasePlan(ctx, planCreator, model.PreviewRequest{
		Service: item.Service, Action: op.Action, Target: op.Target,
		IdempotencyKey: planKey, RequiresDualApproval: childRequiresDual,
	})
	if err != nil {
		_ = engine.failReadyBatchItem(ctx, op.ID, item.ID, "", err, fence...)
		return
	}
	approved, err := engine.approveBatchChildPlan(ctx, op, plan, planCreator, childRequiresDual)
	if err != nil {
		_ = engine.failReadyBatchItem(ctx, op.ID, item.ID, plan.ID, err, fence...)
		return
	}
	task, _, err := engine.ExecuteReleasePlan(ctx, planExecutor, approved.ID, model.ExecutePlanRequest{IdempotencyKey: taskKey})
	if err != nil {
		_ = engine.failReadyBatchItem(ctx, op.ID, item.ID, plan.ID, err, fence...)
		return
	}
	if err := engine.store.UpdateBatchItemCAS(ctx, op.ID, item.ID, model.BatchNodeReady, model.BatchNodeRunning, plan.ID, task.ID, "", fence...); err != nil {
		_ = engine.store.FinishBatchOperation(ctx, op.ID, model.BatchNeedsAttention,
			"批量任务已启动但项目关联失败", redactText(err.Error()), fence...)
	}
}

// batchChildActors preserves the parent's four-party chain for high-risk
// children. Low/medium child plans retain their existing single-operator
// contract because ReleasePlan requires their creator to execute them.
func batchChildActors(op model.BatchOperation, requiresDual bool) (creator, executor string, err error) {
	executor = op.ExecutedByHash
	if executor == "" {
		executor = op.ActorHash
	}
	creator = executor
	if !requiresDual {
		return creator, executor, nil
	}
	if !model.IndependentExecutor(executor, op.ActorHash, op.ApprovedByHash, op.SecondApprovedByHash) {
		return "", "", errors.New("父批量计划缺少完整四方独立身份")
	}
	return op.ActorHash, executor, nil
}

func (engine *Engine) approveBatchChildPlan(
	ctx context.Context,
	op model.BatchOperation,
	plan model.ReleasePlan,
	planCreator string,
	requiresDual bool,
) (model.ReleasePlan, error) {
	approve := func(actor string, current model.ReleasePlan) (model.ReleasePlan, error) {
		return engine.ApproveReleasePlan(ctx, actor, current.ID, model.ApprovePlanRequest{
			Digest: current.Digest, Confirmation: current.ConfirmationPhrase,
		})
	}
	if !requiresDual {
		if plan.RequiresDualApproval {
			return model.ReleasePlan{}, errors.New("批量子计划需要父批量计划未提供的双人批准")
		}
		if plan.ApprovedByHash == "" {
			return approve(planCreator, plan)
		}
		if plan.ApprovedByHash != planCreator {
			return model.ReleasePlan{}, errors.New("批量子计划批准身份与父批量计划不一致")
		}
		return plan, nil
	}
	if !op.RequiresDualApproval || !plan.RequiresDualApproval || plan.ActorHash != op.ActorHash {
		return model.ReleasePlan{}, errors.New("批量子计划审批策略与父批量计划不一致")
	}
	var err error
	if plan.ApprovedByHash == "" {
		plan, err = approve(op.ApprovedByHash, plan)
		if err != nil {
			return model.ReleasePlan{}, err
		}
	} else if plan.ApprovedByHash != op.ApprovedByHash {
		return model.ReleasePlan{}, errors.New("批量子计划第一批准人与父批量计划不一致")
	}
	if plan.SecondApprovedByHash == "" {
		return approve(op.SecondApprovedByHash, plan)
	}
	if plan.SecondApprovedByHash != op.SecondApprovedByHash {
		return model.ReleasePlan{}, errors.New("批量子计划第二批准人与父批量计划不一致")
	}
	return plan, nil
}

func batchItemIdempotencyKey(batchID, itemID, operation string) string {
	sum := sha256.Sum256([]byte(batchID + "\x00" + itemID + "\x00" + operation))
	b := sum[:16]
	// UUID v4 shape keeps the existing validation contract while making the
	// value deterministic for crash/restart replay.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func batchWindowOpen(operation model.BatchOperation, now time.Time) bool {
	if operation.Task.ChangeWindow == nil {
		return true
	}
	return operation.Task.ChangeWindow.Contains(now)
}

func (engine *Engine) runnerIDForService(ctx context.Context, service model.ServiceDefinition) string {
	if service.ServerID == "" {
		return ""
	}
	servers, runners := engine.selectorFleet(ctx)
	server := servers[service.ServerID]
	available := runnersForServer(service.ServerID, server.RunnerID, runners)
	if len(available) == 0 {
		return ""
	}
	return available[0].ID
}

func (engine *Engine) failReadyBatchItem(
	ctx context.Context,
	jobID, itemID, planID string,
	cause error,
	fence ...store.BatchCoordinatorFence,
) error {
	return engine.store.UpdateBatchItemCAS(ctx, jobID, itemID, model.BatchNodeReady,
		model.BatchNodeFailed, planID, "", redactText(cause.Error()), fence...)
}

func (engine *Engine) waitBatchTasks(ctx context.Context, op model.BatchOperation, items []model.BatchItem, lease ...*batchCoordinatorLease) bool {
	ticker := time.NewTicker(batchTaskPollInterval)
	defer ticker.Stop()
	var fence []store.BatchCoordinatorFence
	if len(lease) > 0 && lease[0] != nil {
		fence = append(fence, lease[0].fence())
	}
	for {
		if len(lease) > 0 && lease[0] != nil && lease[0].lost.Load() {
			return false
		}
		allDone := true
		for _, item := range items {
			if item.TaskID == "" {
				_ = engine.store.UpdateBatchItemCAS(ctx, op.ID, item.ID, item.State,
					model.BatchNodeFailed, item.PlanID, "", "批量项目缺少关联任务，需人工核对", fence...)
				continue
			}
			task, err := engine.store.GetTask(ctx, item.TaskID)
			if err != nil {
				_ = engine.store.UpdateBatchItemCAS(ctx, op.ID, item.ID, item.State,
					model.BatchNodeFailed, item.PlanID, item.TaskID, "关联任务无法读取，需人工核对", fence...)
				continue
			}
			if !task.State.Terminal() {
				allDone = false
				continue
			}
			if task.State == model.TaskSucceeded {
				if item.PlanID == "" {
					_ = engine.store.UpdateBatchItemCAS(ctx, op.ID, item.ID, model.BatchNodeRunning,
						model.BatchNodeFailed, "", item.TaskID, "关联发布计划缺失，需人工核对", fence...)
					continue
				}
				plan, planErr := engine.store.GetReleasePlan(ctx, item.PlanID)
				if planErr != nil {
					_ = engine.store.UpdateBatchItemCAS(ctx, op.ID, item.ID, model.BatchNodeRunning,
						model.BatchNodeFailed, item.PlanID, item.TaskID, "关联发布计划无法读取，需人工核对", fence...)
					continue
				}
				switch plan.State {
				case model.PlanObserving:
					allDone = false
					if plan.ObservationEndsAt == nil || !time.Now().UTC().Before(*plan.ObservationEndsAt) {
						key := batchItemIdempotencyKey(op.ID, item.ID, "close")
						_, closeErr := engine.CloseReleasePlan(ctx, plan.ActorHash, plan.ID,
							model.ClosePlanRequest{IdempotencyKey: key})
						if closeErr != nil {
							latestPlan, latestErr := engine.store.GetReleasePlan(ctx, plan.ID)
							reason := plan.ClosureReason
							if latestErr == nil {
								reason = latestPlan.ClosureReason
							}
							if strings.Contains(closeErr.Error(), "观察窗口尚未结束") && strings.TrimSpace(reason) == "" {
								// Clock skew between the coordinator and the store is
								// retryable; do not fail the batch item.
								continue
							}
							if strings.TrimSpace(reason) == "" {
								reason = redactText(closeErr.Error())
							}
							if strings.TrimSpace(reason) == "" {
								reason = "计划收口失败，需人工核对"
							}
							_ = engine.store.UpdateBatchItemCAS(ctx, op.ID, item.ID,
								model.BatchNodeRunning, model.BatchNodeFailed, item.PlanID, item.TaskID,
								redactText(reason), fence...)
						}
					}
					continue
				case model.PlanCompleted:
					_ = engine.store.UpdateBatchItemCAS(ctx, op.ID, item.ID, model.BatchNodeRunning, model.BatchNodeSucceeded, "", "", "", fence...)
				case model.PlanNeedsAttention, model.PlanInvalidated:
					_ = engine.store.UpdateBatchItemCAS(ctx, op.ID, item.ID, model.BatchNodeRunning, model.BatchNodeFailed, "", "", plan.ClosureReason, fence...)
				default:
					allDone = false
				}
			} else {
				_ = engine.store.UpdateBatchItemCAS(ctx, op.ID, item.ID, model.BatchNodeRunning, model.BatchNodeFailed, "", "", redactText(task.Error), fence...)
			}
		}
		if allDone {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}
