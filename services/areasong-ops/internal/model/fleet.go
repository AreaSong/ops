package model

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Fleet models the control-plane inventory and the execution graph used for
// multi-server operations. It intentionally contains no transport or storage
// concerns so the same validation can be used by Web, Runner, and tests.

var (
	ErrInvalidFleet          = errors.New("invalid fleet")
	ErrInvalidNode           = errors.New("invalid fleet node")
	ErrInvalidBatchPolicy    = errors.New("invalid batch policy")
	ErrInvalidConcurrency    = errors.New("invalid concurrency policy")
	ErrInvalidFailurePolicy  = errors.New("invalid failure policy")
	ErrInvalidDAG            = errors.New("invalid DAG")
	ErrDAGCycle              = errors.New("DAG contains a cycle")
	ErrInvalidChangeWindow   = errors.New("invalid change window")
	ErrInvalidNodeTransition = errors.New("invalid node state transition")
)

var fleetNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{0,127}$`)
var certificateFingerprintPattern = regexp.MustCompile(`^(?:sha256:)?[a-fA-F0-9]{64}$`)

// NodeState is shared by server and runner registrations. Empty state is not
// considered a valid persisted value; new registrations should use unknown.
type NodeState string

const (
	NodeUnknown  NodeState = "unknown"
	NodeOnline   NodeState = "online"
	NodeOffline  NodeState = "offline"
	NodeDraining NodeState = "draining"
	NodeDisabled NodeState = "disabled"
)

type ServerNodeState = NodeState
type RunnerNodeState = NodeState

const (
	ServerNodeUnknown  = NodeUnknown
	ServerNodeOnline   = NodeOnline
	ServerNodeOffline  = NodeOffline
	ServerNodeDraining = NodeDraining
	ServerNodeDisabled = NodeDisabled
	RunnerNodeUnknown  = NodeUnknown
	RunnerNodeOnline   = NodeOnline
	RunnerNodeOffline  = NodeOffline
	RunnerNodeDraining = NodeDraining
	RunnerNodeDisabled = NodeDisabled
)

func validNodeState(state NodeState) bool {
	switch state {
	case NodeUnknown, NodeOnline, NodeOffline, NodeDraining, NodeDisabled:
		return true
	default:
		return false
	}
}

func (state NodeState) Available() bool {
	return state == NodeOnline
}

func (state NodeState) CanTransition(next NodeState) bool {
	switch state {
	case NodeUnknown:
		return next == NodeOnline || next == NodeOffline || next == NodeDisabled
	case NodeOnline:
		return next == NodeOffline || next == NodeDraining || next == NodeDisabled || next == NodeUnknown
	case NodeOffline:
		return next == NodeOnline || next == NodeDisabled || next == NodeUnknown
	case NodeDraining:
		return next == NodeOnline || next == NodeOffline || next == NodeDisabled || next == NodeUnknown
	case NodeDisabled:
		return next == NodeUnknown
	default:
		return false
	}
}

func CanTransitionFleetNode(from, to NodeState) bool { return from.CanTransition(to) }

// Capability is an optional richer registry entry. Nodes carry the stable
// capability names in Capabilities; details can be supplied by a registry.
type Capability struct {
	Name       string            `json:"name"`
	Version    string            `json:"version,omitempty"`
	ReadOnly   bool              `json:"readOnly,omitempty"`
	Parameters map[string]string `json:"parameters,omitempty"`
}

type CapabilityDefinition = Capability

// LabelSet is useful at call sites while JSON remains a plain object.
type LabelSet map[string]string

func (labels LabelSet) Clone() LabelSet {
	if labels == nil {
		return nil
	}
	copy := make(LabelSet, len(labels))
	for key, value := range labels {
		copy[key] = value
	}
	return copy
}

func (labels LabelSet) Matches(required map[string]string) bool {
	for key, value := range required {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func ValidateLabels(labels map[string]string) error {
	for key, value := range labels {
		if !fleetNamePattern.MatchString(key) {
			return fmt.Errorf("%w: invalid label key %q", ErrInvalidNode, key)
		}
		if strings.TrimSpace(value) == "" || len(value) > 256 {
			return fmt.Errorf("%w: invalid label value for %q", ErrInvalidNode, key)
		}
	}
	return nil
}

func ValidateCapabilities(capabilities []string) error {
	seen := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		if !fleetNamePattern.MatchString(capability) {
			return fmt.Errorf("%w: invalid capability %q", ErrInvalidNode, capability)
		}
		if _, exists := seen[capability]; exists {
			return fmt.Errorf("%w: duplicate capability %q", ErrInvalidNode, capability)
		}
		seen[capability] = struct{}{}
	}
	return nil
}

func ValidateCapabilityDefinitions(capabilities []Capability) error {
	seen := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		if !fleetNamePattern.MatchString(capability.Name) {
			return fmt.Errorf("%w: invalid capability definition %q", ErrInvalidNode, capability.Name)
		}
		if _, exists := seen[capability.Name]; exists {
			return fmt.Errorf("%w: duplicate capability definition %q", ErrInvalidNode, capability.Name)
		}
		seen[capability.Name] = struct{}{}
		if err := ValidateLabels(capability.Parameters); err != nil {
			return err
		}
	}
	return nil
}

type ServerNode struct {
	ID                    string            `json:"id"`
	Hostname              string            `json:"hostname"`
	Environment           string            `json:"environment"`
	Region                string            `json:"region,omitempty"`
	Address               string            `json:"address,omitempty"`
	Labels                map[string]string `json:"labels,omitempty"`
	Capabilities          []string          `json:"capabilities,omitempty"`
	CapabilityDefinitions []Capability      `json:"capabilityDefinitions,omitempty"`
	RunnerID              string            `json:"runnerId,omitempty"`
	State                 NodeState         `json:"state"`
	MaxConcurrency        int               `json:"maxConcurrency,omitempty"`
	LastHeartbeat         *time.Time        `json:"lastHeartbeat,omitempty"`
	DisabledReason        string            `json:"disabledReason,omitempty"`
}

type RunnerNode struct {
	ID                     string            `json:"id"`
	ServerID               string            `json:"serverId"`
	TenantID               string            `json:"tenantId,omitempty"`
	Hostname               string            `json:"hostname,omitempty"`
	Version                string            `json:"version"`
	Revision               string            `json:"revision,omitempty"`
	BinaryDigest           string            `json:"binaryDigest,omitempty"`
	IdentityPayloadVersion int               `json:"identityPayloadVersion,omitempty"`
	Labels                 map[string]string `json:"labels,omitempty"`
	Capabilities           []string          `json:"capabilities,omitempty"`
	CapabilityDefinitions  []Capability      `json:"capabilityDefinitions,omitempty"`
	State                  NodeState         `json:"state"`
	MaxConcurrency         int               `json:"maxConcurrency,omitempty"`
	LastHeartbeat          *time.Time        `json:"lastHeartbeat,omitempty"`
	LeaseExpiresAt         *time.Time        `json:"leaseExpiresAt,omitempty"`
	LeaseGeneration        uint64            `json:"leaseGeneration,omitempty"`
	CertificateFingerprint string            `json:"certificateFingerprint,omitempty"`
	HeartbeatPublicKey     string            `json:"heartbeatPublicKey,omitempty"`
	DisabledReason         string            `json:"disabledReason,omitempty"`
	LastHeartbeatNonce     string            `json:"-"`
	LeaseToken             string            `json:"-"`
}

func (node ServerNode) Validate() error {
	if !fleetNamePattern.MatchString(node.ID) || strings.TrimSpace(node.Hostname) == "" ||
		strings.TrimSpace(node.Environment) == "" {
		return fmt.Errorf("%w: server id, hostname, and environment are required", ErrInvalidNode)
	}
	if node.RunnerID != "" && !fleetNamePattern.MatchString(node.RunnerID) {
		return fmt.Errorf("%w: invalid runner id %q", ErrInvalidNode, node.RunnerID)
	}
	if !validNodeState(node.State) {
		return fmt.Errorf("%w: invalid server state %q", ErrInvalidNode, node.State)
	}
	if node.MaxConcurrency < 0 {
		return fmt.Errorf("%w: server max concurrency cannot be negative", ErrInvalidNode)
	}
	if err := ValidateLabels(node.Labels); err != nil {
		return err
	}
	if err := ValidateCapabilities(node.Capabilities); err != nil {
		return err
	}
	return ValidateCapabilityDefinitions(node.CapabilityDefinitions)
}

func ValidateServerNode(node ServerNode) error { return node.Validate() }

func (node RunnerNode) Validate() error {
	if !fleetNamePattern.MatchString(node.ID) || !fleetNamePattern.MatchString(node.ServerID) ||
		strings.TrimSpace(node.Version) == "" {
		return fmt.Errorf("%w: runner id, server id, and version are required", ErrInvalidNode)
	}
	if !validNodeState(node.State) {
		return fmt.Errorf("%w: invalid runner state %q", ErrInvalidNode, node.State)
	}
	if node.MaxConcurrency < 0 {
		return fmt.Errorf("%w: runner max concurrency cannot be negative", ErrInvalidNode)
	}
	if err := ValidateLabels(node.Labels); err != nil {
		return err
	}
	if err := ValidateCapabilities(node.Capabilities); err != nil {
		return err
	}
	if node.CertificateFingerprint != "" && !certificateFingerprintPattern.MatchString(node.CertificateFingerprint) {
		return fmt.Errorf("%w: invalid certificate fingerprint", ErrInvalidNode)
	}
	if node.HeartbeatPublicKey != "" {
		key, err := base64.StdEncoding.Strict().DecodeString(node.HeartbeatPublicKey)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return fmt.Errorf("%w: invalid heartbeat public key", ErrInvalidNode)
		}
	}
	return ValidateCapabilityDefinitions(node.CapabilityDefinitions)
}

func ValidateRunnerNode(node RunnerNode) error { return node.Validate() }

func (node RunnerNode) AvailableAt(now time.Time, heartbeatTimeout time.Duration) bool {
	if node.State != NodeOnline || node.LastHeartbeat == nil || heartbeatTimeout <= 0 {
		return false
	}
	if node.LeaseExpiresAt != nil && !now.Before(*node.LeaseExpiresAt) {
		return false
	}
	// A lease expires at the deadline, not after it. Treat an exact timeout
	// boundary as unavailable so callers cannot start a new mutation with a
	// heartbeat that is already due for renewal.
	return node.LastHeartbeat.After(now.Add(-heartbeatTimeout))
}

type Fleet struct {
	Servers   []ServerNode `json:"servers"`
	Runners   []RunnerNode `json:"runners"`
	CanManage bool         `json:"canManage"`
}

func (fleet Fleet) Validate() error {
	serverIDs := make(map[string]struct{}, len(fleet.Servers))
	for _, server := range fleet.Servers {
		if err := server.Validate(); err != nil {
			return err
		}
		if _, exists := serverIDs[server.ID]; exists {
			return fmt.Errorf("%w: duplicate server id %q", ErrInvalidFleet, server.ID)
		}
		serverIDs[server.ID] = struct{}{}
	}
	runnerIDs := make(map[string]struct{}, len(fleet.Runners))
	for _, runner := range fleet.Runners {
		if err := runner.Validate(); err != nil {
			return err
		}
		if _, exists := runnerIDs[runner.ID]; exists {
			return fmt.Errorf("%w: duplicate runner id %q", ErrInvalidFleet, runner.ID)
		}
		if _, exists := serverIDs[runner.ServerID]; !exists {
			return fmt.Errorf("%w: runner %q references unknown server %q", ErrInvalidFleet, runner.ID, runner.ServerID)
		}
		runnerIDs[runner.ID] = struct{}{}
	}
	for _, server := range fleet.Servers {
		if server.RunnerID != "" {
			runner, exists := findRunner(fleet.Runners, server.RunnerID)
			if !exists || runner.ServerID != server.ID {
				return fmt.Errorf("%w: server %q runner association is inconsistent", ErrInvalidFleet, server.ID)
			}
		}
	}
	return nil
}

func ValidateFleet(fleet Fleet) error { return fleet.Validate() }

func findRunner(runners []RunnerNode, id string) (RunnerNode, bool) {
	for _, runner := range runners {
		if runner.ID == id {
			return runner, true
		}
	}
	return RunnerNode{}, false
}

type NodeSelector struct {
	IDs               []string          `json:"ids,omitempty"`
	MatchLabels       map[string]string `json:"matchLabels,omitempty"`
	MatchCapabilities []string          `json:"matchCapabilities,omitempty"`
	ExcludeIDs        []string          `json:"excludeIds,omitempty"`
}

func (selector NodeSelector) Validate() error {
	seen := make(map[string]struct{}, len(selector.IDs)+len(selector.ExcludeIDs))
	for _, id := range append(append([]string{}, selector.IDs...), selector.ExcludeIDs...) {
		if !fleetNamePattern.MatchString(id) {
			return fmt.Errorf("%w: invalid selector id %q", ErrInvalidNode, id)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("%w: duplicate selector id %q", ErrInvalidNode, id)
		}
		seen[id] = struct{}{}
	}
	if err := ValidateLabels(selector.MatchLabels); err != nil {
		return err
	}
	return ValidateCapabilities(selector.MatchCapabilities)
}

func (selector NodeSelector) matches(id string, labels map[string]string, capabilities []string) bool {
	for _, excluded := range selector.ExcludeIDs {
		if excluded == id {
			return false
		}
	}
	if len(selector.IDs) > 0 {
		found := false
		for _, selected := range selector.IDs {
			if selected == id {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if !LabelSet(labels).Matches(selector.MatchLabels) {
		return false
	}
	capabilitiesSet := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		capabilitiesSet[capability] = struct{}{}
	}
	for _, required := range selector.MatchCapabilities {
		if _, exists := capabilitiesSet[required]; !exists {
			return false
		}
	}
	return true
}

func (selector NodeSelector) MatchesServer(node ServerNode) bool {
	return selector.matches(node.ID, node.Labels, node.Capabilities)
}

func (selector NodeSelector) MatchesRunner(node RunnerNode) bool {
	return selector.matches(node.ID, node.Labels, node.Capabilities)
}

func SelectServerNodes(nodes []ServerNode, selector NodeSelector) ([]ServerNode, error) {
	if err := selector.Validate(); err != nil {
		return nil, err
	}
	selected := make([]ServerNode, 0, len(nodes))
	for _, node := range nodes {
		if selector.MatchesServer(node) {
			selected = append(selected, node)
		}
	}
	return selected, nil
}

func SelectRunnerNodes(nodes []RunnerNode, selector NodeSelector) ([]RunnerNode, error) {
	if err := selector.Validate(); err != nil {
		return nil, err
	}
	selected := make([]RunnerNode, 0, len(nodes))
	for _, node := range nodes {
		if selector.MatchesRunner(node) {
			selected = append(selected, node)
		}
	}
	return selected, nil
}

type BatchStrategy string

const (
	BatchSerial     BatchStrategy = "serial"
	BatchFixed      BatchStrategy = "fixed"
	BatchPercentage BatchStrategy = "percentage"
	BatchCanary     BatchStrategy = "canary"

	BatchStrategySerial     = BatchSerial
	BatchStrategyFixed      = BatchFixed
	BatchStrategyPercentage = BatchPercentage
	BatchStrategyCanary     = BatchCanary
)

type BatchPolicy struct {
	Strategy           BatchStrategy `json:"strategy"`
	BatchSize          int           `json:"batchSize,omitempty"`
	BatchPercentage    int           `json:"batchPercentage,omitempty"`
	CanarySize         int           `json:"canarySize,omitempty"`
	CanaryPercentage   int           `json:"canaryPercentage,omitempty"`
	PauseSeconds       int           `json:"pauseSeconds,omitempty"`
	ObservationSeconds int           `json:"observationSeconds,omitempty"`
}

type BatchPlan = BatchPolicy

func (policy BatchPolicy) Validate() error {
	if policy.Strategy == "" {
		return fmt.Errorf("%w: strategy is required", ErrInvalidBatchPolicy)
	}
	if policy.BatchSize < 0 || policy.BatchPercentage < 0 || policy.CanarySize < 0 || policy.CanaryPercentage < 0 ||
		policy.PauseSeconds < 0 || policy.ObservationSeconds < 0 {
		return fmt.Errorf("%w: numeric values cannot be negative", ErrInvalidBatchPolicy)
	}
	switch policy.Strategy {
	case BatchSerial:
		if policy.BatchSize > 1 || policy.BatchPercentage > 0 || policy.CanarySize > 0 || policy.CanaryPercentage > 0 {
			return fmt.Errorf("%w: serial strategy cannot specify batch sizing", ErrInvalidBatchPolicy)
		}
	case BatchFixed:
		if policy.BatchSize == 0 || policy.BatchPercentage > 0 || policy.CanarySize > 0 || policy.CanaryPercentage > 0 {
			return fmt.Errorf("%w: fixed strategy requires batchSize only", ErrInvalidBatchPolicy)
		}
	case BatchPercentage:
		if policy.BatchPercentage < 1 || policy.BatchPercentage > 100 || policy.BatchSize > 0 || policy.CanarySize > 0 || policy.CanaryPercentage > 0 {
			return fmt.Errorf("%w: percentage strategy requires batchPercentage from 1 to 100", ErrInvalidBatchPolicy)
		}
	case BatchCanary:
		if (policy.CanarySize == 0) == (policy.CanaryPercentage == 0) ||
			(policy.CanaryPercentage > 100) || (policy.BatchSize == 0 && policy.BatchPercentage == 0) ||
			(policy.BatchSize > 0 && policy.BatchPercentage > 0) {
			return fmt.Errorf("%w: canary requires one canary size, then one rollout size", ErrInvalidBatchPolicy)
		}
	default:
		return fmt.Errorf("%w: unknown strategy %q", ErrInvalidBatchPolicy, policy.Strategy)
	}
	return nil
}

func ValidateBatchPolicy(policy BatchPolicy) error { return policy.Validate() }

func (policy BatchPolicy) batchWidth(total int, canary bool) int {
	if canary {
		if policy.CanarySize > 0 {
			return policy.CanarySize
		}
		return ceilPercent(total, policy.CanaryPercentage)
	}
	switch policy.Strategy {
	case BatchSerial:
		return 1
	case BatchFixed, BatchCanary:
		if policy.BatchSize > 0 {
			return policy.BatchSize
		}
		return ceilPercent(total, policy.BatchPercentage)
	case BatchPercentage:
		return ceilPercent(total, policy.BatchPercentage)
	default:
		return 0
	}
}

func ceilPercent(total, percentage int) int {
	if total <= 0 || percentage <= 0 {
		return 0
	}
	width := (total*percentage + 99) / 100
	if width < 1 {
		return 1
	}
	return width
}

// Partition returns deterministic contiguous batches and never emits an empty
// batch. It is pure: the input slice is not modified.
func (policy BatchPolicy) Partition(targetIDs []string) ([][]string, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if len(targetIDs) == 0 {
		return [][]string{}, nil
	}
	seen := make(map[string]struct{}, len(targetIDs))
	for _, target := range targetIDs {
		if !fleetNamePattern.MatchString(target) {
			return nil, fmt.Errorf("%w: invalid target id %q", ErrInvalidBatchPolicy, target)
		}
		if _, exists := seen[target]; exists {
			return nil, fmt.Errorf("%w: duplicate target id %q", ErrInvalidBatchPolicy, target)
		}
		seen[target] = struct{}{}
	}
	width := policy.batchWidth(len(targetIDs), false)
	canary := policy.Strategy == BatchCanary
	result := make([][]string, 0)
	remaining := targetIDs
	if canary {
		width = policy.batchWidth(len(targetIDs), true)
		result = append(result, append([]string(nil), remaining[:min(width, len(remaining))]...))
		remaining = remaining[min(width, len(remaining)):]
		if len(remaining) == 0 {
			return result, nil
		}
		width = policy.batchWidth(len(remaining), false)
	}
	if width < 1 {
		return nil, fmt.Errorf("%w: computed batch width is zero", ErrInvalidBatchPolicy)
	}
	for len(remaining) > 0 {
		count := min(width, len(remaining))
		result = append(result, append([]string(nil), remaining[:count]...))
		remaining = remaining[count:]
	}
	return result, nil
}

func PartitionTargets(targetIDs []string, policy BatchPolicy) ([][]string, error) {
	return policy.Partition(targetIDs)
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

type ConcurrencyScope string

const (
	ConcurrencyGlobal    ConcurrencyScope = "global"
	ConcurrencyPerRunner ConcurrencyScope = "per_runner"
	ConcurrencyPerServer ConcurrencyScope = "per_server"
)

type ConcurrencyPolicy struct {
	Scope         ConcurrencyScope `json:"scope"`
	MaxConcurrent int              `json:"maxConcurrent"`
	PerRunner     int              `json:"perRunner,omitempty"`
	PerServer     int              `json:"perServer,omitempty"`
	QueueLimit    int              `json:"queueLimit,omitempty"`
}

func (policy ConcurrencyPolicy) Validate() error {
	if policy.MaxConcurrent < 1 {
		return fmt.Errorf("%w: maxConcurrent must be positive", ErrInvalidConcurrency)
	}
	if policy.MaxConcurrent > 10000 || policy.PerRunner < 0 || policy.PerServer < 0 || policy.QueueLimit < 0 {
		return fmt.Errorf("%w: concurrency limit is out of range", ErrInvalidConcurrency)
	}
	switch policy.Scope {
	case ConcurrencyGlobal:
	case ConcurrencyPerRunner:
		if policy.PerRunner == 0 {
			return fmt.Errorf("%w: perRunner scope requires perRunner", ErrInvalidConcurrency)
		}
	case ConcurrencyPerServer:
		if policy.PerServer == 0 {
			return fmt.Errorf("%w: perServer scope requires perServer", ErrInvalidConcurrency)
		}
	default:
		return fmt.Errorf("%w: unknown scope %q", ErrInvalidConcurrency, policy.Scope)
	}
	return nil
}

func ValidateConcurrencyPolicy(policy ConcurrencyPolicy) error { return policy.Validate() }

type FailurePolicy string

const (
	FailureStop           FailurePolicy = "stop"
	FailureContinue       FailurePolicy = "continue"
	FailureRollback       FailurePolicy = "rollback"
	FailurePause          FailurePolicy = "pause"
	FailureNeedsAttention FailurePolicy = "needs_attention"

	FailurePolicyStop           = FailureStop
	FailurePolicyContinue       = FailureContinue
	FailurePolicyRollback       = FailureRollback
	FailurePolicyPause          = FailurePause
	FailurePolicyNeedsAttention = FailureNeedsAttention
)

type FailureStrategy = FailurePolicy

const (
	FailureStrategyStop           = FailureStop
	FailureStrategyContinue       = FailureContinue
	FailureStrategyRollback       = FailureRollback
	FailureStrategyPause          = FailurePause
	FailureStrategyNeedsAttention = FailureNeedsAttention
)

type FailurePolicyConfig struct {
	Policy            FailurePolicy `json:"policy"`
	MaxFailures       int           `json:"maxFailures,omitempty"`
	RollbackOnFailure bool          `json:"rollbackOnFailure,omitempty"`
}

func (policy FailurePolicy) Validate() error {
	switch policy {
	case FailureStop, FailureContinue, FailureRollback, FailurePause, FailureNeedsAttention:
		return nil
	default:
		return fmt.Errorf("%w: unknown policy %q", ErrInvalidFailurePolicy, policy)
	}
}

func (policy FailurePolicyConfig) Validate() error {
	if err := policy.Policy.Validate(); err != nil {
		return err
	}
	if policy.MaxFailures < 0 {
		return fmt.Errorf("%w: maxFailures cannot be negative", ErrInvalidFailurePolicy)
	}
	if policy.RollbackOnFailure && policy.Policy != FailureRollback {
		return fmt.Errorf("%w: rollbackOnFailure requires rollback policy", ErrInvalidFailurePolicy)
	}
	return nil
}

func (policy FailurePolicyConfig) ShouldStop(failures int) bool {
	if failures <= 0 || policy.Validate() != nil {
		return false
	}
	if policy.Policy == FailureContinue {
		return policy.MaxFailures > 0 && failures >= policy.MaxFailures
	}
	limit := policy.MaxFailures
	if limit == 0 {
		limit = 1
	}
	return failures >= limit
}

type ChangeWindow struct {
	ID                string    `json:"id,omitempty"`
	StartAt           time.Time `json:"startAt"`
	EndAt             time.Time `json:"endAt"`
	Timezone          string    `json:"timezone,omitempty"`
	Reason            string    `json:"reason,omitempty"`
	ApprovalRequired  bool      `json:"approvalRequired,omitempty"`
	EmergencyOverride bool      `json:"emergencyOverride,omitempty"`
}

func (window ChangeWindow) Validate() error {
	if !window.StartAt.Before(window.EndAt) {
		return fmt.Errorf("%w: startAt must be before endAt", ErrInvalidChangeWindow)
	}
	if window.Timezone != "" {
		if _, err := time.LoadLocation(window.Timezone); err != nil {
			return fmt.Errorf("%w: invalid timezone %q", ErrInvalidChangeWindow, window.Timezone)
		}
	}
	if window.ID != "" && !fleetNamePattern.MatchString(window.ID) {
		return fmt.Errorf("%w: invalid window id", ErrInvalidChangeWindow)
	}
	return nil
}

func ValidateChangeWindow(window ChangeWindow) error { return window.Validate() }

func (window ChangeWindow) Contains(at time.Time) bool {
	if window.Validate() != nil {
		return false
	}
	return !at.Before(window.StartAt) && at.Before(window.EndAt)
}

func (window ChangeWindow) IsOpen(at time.Time) bool { return window.Contains(at) }

type BatchNodeState string

const (
	BatchNodePending     BatchNodeState = "pending"
	BatchNodeReady       BatchNodeState = "ready"
	BatchNodeRunning     BatchNodeState = "running"
	BatchNodeSucceeded   BatchNodeState = "succeeded"
	BatchNodeFailed      BatchNodeState = "failed"
	BatchNodeSkipped     BatchNodeState = "skipped"
	BatchNodeBlocked     BatchNodeState = "blocked"
	BatchNodeCancelled   BatchNodeState = "cancelled"
	BatchNodeRollingBack BatchNodeState = "rolling_back"
	BatchNodeRolledBack  BatchNodeState = "rolled_back"
	BatchNodeUnknown     BatchNodeState = "unknown"
)

type DAGNodeState = BatchNodeState

const (
	DAGNodePending     = BatchNodePending
	DAGNodeReady       = BatchNodeReady
	DAGNodeRunning     = BatchNodeRunning
	DAGNodeSucceeded   = BatchNodeSucceeded
	DAGNodeFailed      = BatchNodeFailed
	DAGNodeSkipped     = BatchNodeSkipped
	DAGNodeBlocked     = BatchNodeBlocked
	DAGNodeCancelled   = BatchNodeCancelled
	DAGNodeRollingBack = BatchNodeRollingBack
	DAGNodeRolledBack  = BatchNodeRolledBack
	DAGNodeUnknown     = BatchNodeUnknown
)

func (state BatchNodeState) Terminal() bool {
	switch state {
	case BatchNodeSucceeded, BatchNodeFailed, BatchNodeSkipped, BatchNodeBlocked, BatchNodeCancelled, BatchNodeRolledBack, BatchNodeUnknown:
		return true
	default:
		return false
	}
}

func (state BatchNodeState) CanTransition(next BatchNodeState) bool {
	switch state {
	case BatchNodePending:
		return next == BatchNodeReady || next == BatchNodeCancelled || next == BatchNodeBlocked
	case BatchNodeReady:
		return next == BatchNodeRunning || next == BatchNodeCancelled || next == BatchNodeBlocked
	case BatchNodeRunning:
		return next == BatchNodeSucceeded || next == BatchNodeFailed || next == BatchNodeRollingBack || next == BatchNodeUnknown
	case BatchNodeRollingBack:
		return next == BatchNodeRolledBack || next == BatchNodeFailed || next == BatchNodeUnknown
	default:
		return false
	}
}

func CanTransitionDAGNode(from, to BatchNodeState) bool { return from.CanTransition(to) }

// CanTransition is the common node transition entry point used by the
// scheduler. BatchTaskState is accepted as well so callers do not need a
// second dispatch helper when they validate the enclosing task state.
func CanTransition(from, to any) bool {
	switch current := from.(type) {
	case NodeState:
		next, ok := to.(NodeState)
		return ok && current.CanTransition(next)
	case BatchNodeState:
		next, ok := to.(BatchNodeState)
		return ok && current.CanTransition(next)
	case BatchTaskState:
		next, ok := to.(BatchTaskState)
		return ok && current.CanTransition(next)
	default:
		return false
	}
}

func ValidateNodeTransition(from, to BatchNodeState) error {
	if !from.CanTransition(to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidNodeTransition, from, to)
	}
	return nil
}

type DAGNode struct {
	ID           string         `json:"id"`
	Action       string         `json:"action,omitempty"`
	Capability   string         `json:"capability,omitempty"`
	TargetID     string         `json:"targetId,omitempty"`
	Dependencies []string       `json:"dependencies,omitempty"`
	DependsOn    []string       `json:"dependsOn,omitempty"`
	State        BatchNodeState `json:"state"`
	Attempts     int            `json:"attempts,omitempty"`
	Error        string         `json:"error,omitempty"`
	StartedAt    *time.Time     `json:"startedAt,omitempty"`
	FinishedAt   *time.Time     `json:"finishedAt,omitempty"`
}

type DAG struct {
	Nodes []DAGNode `json:"nodes"`
}

func (dag DAG) Validate() error { return ValidateDAG(dag.Nodes) }

func (dag DAG) ReadyNodes() ([]DAGNode, error) { return SelectReadyNodes(dag.Nodes) }

func (dag DAG) TopologicalOrder() ([]DAGNode, error) { return TopologicalOrder(dag.Nodes) }

type BatchTaskState string

const (
	BatchTaskPending        BatchTaskState = "pending"
	BatchTaskPlanning       BatchTaskState = "planning"
	BatchTaskRunning        BatchTaskState = "running"
	BatchTaskPaused         BatchTaskState = "paused"
	BatchTaskSucceeded      BatchTaskState = "succeeded"
	BatchTaskFailed         BatchTaskState = "failed"
	BatchTaskRollingBack    BatchTaskState = "rolling_back"
	BatchTaskRolledBack     BatchTaskState = "rolled_back"
	BatchTaskCancelled      BatchTaskState = "cancelled"
	BatchTaskNeedsAttention BatchTaskState = "needs_attention"
	BatchTaskUnknown        BatchTaskState = "unknown"
)

func (state BatchTaskState) Terminal() bool {
	switch state {
	case BatchTaskSucceeded, BatchTaskFailed, BatchTaskRolledBack, BatchTaskCancelled, BatchTaskNeedsAttention, BatchTaskUnknown:
		return true
	default:
		return false
	}
}

func (state BatchTaskState) CanTransition(next BatchTaskState) bool {
	switch state {
	case BatchTaskPending:
		return next == BatchTaskPlanning || next == BatchTaskCancelled
	case BatchTaskPlanning:
		return next == BatchTaskRunning || next == BatchTaskCancelled || next == BatchTaskFailed
	case BatchTaskRunning:
		return next == BatchTaskPaused || next == BatchTaskSucceeded || next == BatchTaskFailed || next == BatchTaskRollingBack || next == BatchTaskNeedsAttention || next == BatchTaskUnknown
	case BatchTaskPaused:
		return next == BatchTaskRunning || next == BatchTaskCancelled || next == BatchTaskRollingBack || next == BatchTaskNeedsAttention
	case BatchTaskRollingBack:
		return next == BatchTaskRolledBack || next == BatchTaskNeedsAttention || next == BatchTaskUnknown
	default:
		return false
	}
}

func CanTransitionBatchTask(from, to BatchTaskState) bool { return from.CanTransition(to) }

type BatchTask struct {
	ID             string               `json:"id"`
	Name           string               `json:"name,omitempty"`
	Action         string               `json:"action,omitempty"`
	Capability     string               `json:"capability,omitempty"`
	TargetSelector NodeSelector         `json:"targetSelector,omitempty"`
	TargetIDs      []string             `json:"targetIds,omitempty"`
	Nodes          []DAGNode            `json:"nodes"`
	BatchPolicy    BatchPolicy          `json:"batchPolicy"`
	Concurrency    ConcurrencyPolicy    `json:"concurrency"`
	FailurePolicy  FailurePolicy        `json:"failurePolicy"`
	FailureConfig  *FailurePolicyConfig `json:"failureConfig,omitempty"`
	ChangeWindow   *ChangeWindow        `json:"changeWindow,omitempty"`
	State          BatchTaskState       `json:"state"`
	Summary        string               `json:"summary,omitempty"`
	Error          string               `json:"error,omitempty"`
	CreatedAt      time.Time            `json:"createdAt"`
	StartedAt      *time.Time           `json:"startedAt,omitempty"`
	FinishedAt     *time.Time           `json:"finishedAt,omitempty"`
}

type BatchJob = BatchTask
type OperationTask = BatchTask
type BatchTaskNode = DAGNode

func (node DAGNode) dependencyIDs() []string {
	dependencies := append([]string(nil), node.Dependencies...)
	for _, dependency := range node.DependsOn {
		found := false
		for _, existing := range dependencies {
			if existing == dependency {
				found = true
				break
			}
		}
		if !found {
			dependencies = append(dependencies, dependency)
		}
	}
	return dependencies
}

func ValidateDAG(nodes []DAGNode) error {
	if len(nodes) == 0 {
		return fmt.Errorf("%w: DAG must contain at least one node", ErrInvalidDAG)
	}
	byID := make(map[string]DAGNode, len(nodes))
	for _, node := range nodes {
		if !fleetNamePattern.MatchString(node.ID) {
			return fmt.Errorf("%w: invalid node id %q", ErrInvalidDAG, node.ID)
		}
		if _, exists := byID[node.ID]; exists {
			return fmt.Errorf("%w: duplicate node id %q", ErrInvalidDAG, node.ID)
		}
		if node.State == "" {
			return fmt.Errorf("%w: node %q has empty state", ErrInvalidDAG, node.ID)
		}
		if !validBatchNodeState(node.State) {
			return fmt.Errorf("%w: node %q has invalid state", ErrInvalidDAG, node.ID)
		}
		if node.Attempts < 0 {
			return fmt.Errorf("%w: node %q attempts cannot be negative", ErrInvalidDAG, node.ID)
		}
		byID[node.ID] = node
	}
	for _, node := range nodes {
		seen := make(map[string]struct{})
		for _, dependency := range node.dependencyIDs() {
			if dependency == node.ID {
				return fmt.Errorf("%w: node %q depends on itself", ErrInvalidDAG, node.ID)
			}
			if _, exists := byID[dependency]; !exists {
				return fmt.Errorf("%w: node %q references unknown dependency %q", ErrInvalidDAG, node.ID, dependency)
			}
			if _, exists := seen[dependency]; exists {
				return fmt.Errorf("%w: node %q repeats dependency %q", ErrInvalidDAG, node.ID, dependency)
			}
			seen[dependency] = struct{}{}
		}
	}
	visiting := make(map[string]bool, len(nodes))
	visited := make(map[string]bool, len(nodes))
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("%w: at node %q", ErrDAGCycle, id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, dependency := range byID[id].dependencyIDs() {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	for _, node := range nodes {
		if err := visit(node.ID); err != nil {
			return err
		}
	}
	return nil
}

func validBatchNodeState(state BatchNodeState) bool {
	switch state {
	case BatchNodePending, BatchNodeReady, BatchNodeRunning, BatchNodeSucceeded, BatchNodeFailed, BatchNodeSkipped, BatchNodeBlocked, BatchNodeCancelled, BatchNodeRollingBack, BatchNodeRolledBack, BatchNodeUnknown:
		return true
	default:
		return false
	}
}

func (task BatchTask) Validate() error {
	if !fleetNamePattern.MatchString(task.ID) {
		return fmt.Errorf("%w: invalid task id", ErrInvalidFleet)
	}
	if strings.TrimSpace(task.Action) == "" && strings.TrimSpace(task.Capability) == "" {
		return fmt.Errorf("%w: action or capability is required", ErrInvalidFleet)
	}
	if len(task.TargetIDs) == 0 && len(task.TargetSelector.IDs) == 0 && len(task.TargetSelector.MatchLabels) == 0 && len(task.TargetSelector.MatchCapabilities) == 0 {
		return fmt.Errorf("%w: target selector or target ids are required", ErrInvalidFleet)
	}
	if err := task.TargetSelector.Validate(); err != nil {
		return err
	}
	if err := validateTargetIDs(task.TargetIDs); err != nil {
		return err
	}
	nodes := task.Nodes
	if err := ValidateDAG(nodes); err != nil {
		return err
	}
	if err := task.BatchPolicy.Validate(); err != nil {
		return err
	}
	if len(task.TargetIDs) > 0 {
		if _, err := task.BatchPolicy.Partition(task.TargetIDs); err != nil {
			return err
		}
	}
	if err := task.Concurrency.Validate(); err != nil {
		return err
	}
	if err := task.FailurePolicy.Validate(); err != nil {
		return err
	}
	if task.FailureConfig != nil {
		if err := task.FailureConfig.Validate(); err != nil {
			return err
		}
		if task.FailureConfig.Policy != task.FailurePolicy {
			return fmt.Errorf("%w: failure policy and failure config disagree", ErrInvalidFleet)
		}
	}
	if task.ChangeWindow != nil {
		if err := task.ChangeWindow.Validate(); err != nil {
			return err
		}
	}
	for _, node := range nodes {
		if node.TargetID != "" && len(task.TargetIDs) > 0 && !containsString(task.TargetIDs, node.TargetID) {
			return fmt.Errorf("%w: DAG node %q references target outside task", ErrInvalidFleet, node.ID)
		}
	}
	if task.State != "" && !validBatchTaskState(task.State) {
		return fmt.Errorf("%w: invalid batch task state %q", ErrInvalidFleet, task.State)
	}
	return nil
}

func ValidateBatchTask(task BatchTask) error { return task.Validate() }

func SelectTargets(fleet Fleet, selector NodeSelector) ([]ServerNode, error) {
	if err := fleet.Validate(); err != nil {
		return nil, err
	}
	return SelectServerNodes(fleet.Servers, selector)
}

func IsWithinChangeWindow(window ChangeWindow, at time.Time) bool {
	return window.Contains(at)
}

func validateTargetIDs(targetIDs []string) error {
	seen := make(map[string]struct{}, len(targetIDs))
	for _, id := range targetIDs {
		if !fleetNamePattern.MatchString(id) {
			return fmt.Errorf("%w: invalid target id %q", ErrInvalidFleet, id)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("%w: duplicate target id %q", ErrInvalidFleet, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func validBatchTaskState(state BatchTaskState) bool {
	switch state {
	case BatchTaskPending, BatchTaskPlanning, BatchTaskRunning, BatchTaskPaused, BatchTaskSucceeded, BatchTaskFailed, BatchTaskRollingBack, BatchTaskRolledBack, BatchTaskCancelled, BatchTaskNeedsAttention, BatchTaskUnknown:
		return true
	default:
		return false
	}
}

// SelectReadyNodes returns nodes whose dependencies have all succeeded. It
// preserves input order, which gives deterministic scheduling and audit output.
func SelectReadyNodes(nodes []DAGNode) ([]DAGNode, error) {
	if err := ValidateDAG(nodes); err != nil {
		return nil, err
	}
	byID := make(map[string]DAGNode, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	ready := make([]DAGNode, 0)
	for _, node := range nodes {
		if node.State != BatchNodePending && node.State != BatchNodeReady {
			continue
		}
		dependenciesReady := true
		for _, dependency := range node.dependencyIDs() {
			if byID[dependency].State != BatchNodeSucceeded {
				dependenciesReady = false
				break
			}
		}
		if dependenciesReady {
			ready = append(ready, node)
		}
	}
	return ready, nil
}

func ReadyNodeIDs(nodes []DAGNode) ([]string, error) {
	ready, err := SelectReadyNodes(nodes)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(ready))
	for _, node := range ready {
		ids = append(ids, node.ID)
	}
	return ids, nil
}

func TopologicalOrder(nodes []DAGNode) ([]DAGNode, error) {
	if err := ValidateDAG(nodes); err != nil {
		return nil, err
	}
	byID := make(map[string]DAGNode, len(nodes))
	indegree := make(map[string]int, len(nodes))
	dependents := make(map[string][]string, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
		indegree[node.ID] = len(node.dependencyIDs())
		for _, dependency := range node.dependencyIDs() {
			dependents[dependency] = append(dependents[dependency], node.ID)
		}
	}
	ready := make([]string, 0)
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	order := make([]DAGNode, 0, len(nodes))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, byID[id])
		for _, dependent := range dependents[id] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
		sort.Strings(ready)
	}
	if len(order) != len(nodes) {
		return nil, ErrDAGCycle
	}
	return order, nil
}
