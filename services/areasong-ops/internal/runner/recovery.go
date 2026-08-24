package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func phaseSemantics(action model.ActionDefinition, phase string) model.PhaseSemantics {
	return model.EffectivePhaseSemantics(action, phase)
}

func mutationSemantics(semantics model.PhaseSemantics) bool {
	return semantics.Effect == "runtime_mutation" || semantics.Effect == "data_mutation"
}

func (engine *Engine) persistRecoveryPoint(
	ctx context.Context,
	task model.Task,
	service model.ServiceDefinition,
	evidence *model.RecoveryPointEvidence,
) (model.RecoveryPoint, error) {
	policy := service.RecoveryPointPolicy
	if policy == nil {
		return model.RecoveryPoint{}, errors.New("服务缺少恢复点策略")
	}
	if evidence == nil {
		return model.RecoveryPoint{}, errors.New("备份阶段未返回恢复点证据")
	}
	if evidence.SchemaVersion != 1 || evidence.Service != task.Service || evidence.TaskID != task.ID {
		return model.RecoveryPoint{}, errors.New("恢复点身份与当前任务不匹配")
	}
	now := time.Now().UTC()
	if evidence.CreatedAt.Before(now.Add(-30*time.Minute)) || evidence.CreatedAt.After(now.Add(time.Minute)) {
		return model.RecoveryPoint{}, errors.New("恢复点不在本次任务的新鲜时间窗口内")
	}
	if err := engine.validateRecoveryArtifacts(*evidence, policy.RequiredArtifactRoles); err != nil {
		return model.RecoveryPoint{}, err
	}
	expectedBeforeDigest, err := canonicalDigest(task.Snapshot)
	if err != nil {
		return model.RecoveryPoint{}, err
	}
	tenantID := service.TenantID
	if tenantID == "" && engine.catalog.Access != nil {
		tenantID = engine.catalog.Access.DefaultTenant
	}
	if tenantID == "" {
		tenantID = "default"
	}
	serverID := service.ServerID
	normalized := *evidence
	normalized.Artifacts = append([]model.RecoveryArtifact(nil), evidence.Artifacts...)
	sort.Slice(normalized.Artifacts, func(left, right int) bool {
		return normalized.Artifacts[left].Role < normalized.Artifacts[right].Role
	})
	if normalized.TenantID != "" && normalized.TenantID != tenantID {
		return model.RecoveryPoint{}, errors.New("恢复点租户与服务租户不一致")
	}
	if normalized.ServerID != "" && normalized.ServerID != serverID {
		return model.RecoveryPoint{}, errors.New("恢复点服务器与服务绑定不一致")
	}
	if normalized.ExpectedBeforeDigest != "" && normalized.ExpectedBeforeDigest != expectedBeforeDigest {
		return model.RecoveryPoint{}, errors.New("恢复点变更前身份摘要不一致")
	}
	normalized.TenantID, normalized.ServerID = tenantID, serverID
	normalized.ExpectedBeforeDigest = expectedBeforeDigest
	normalized.BindingDigest = ""
	unsignedDigest, err := canonicalDigest(normalized)
	if err != nil {
		return model.RecoveryPoint{}, err
	}
	roles := append([]string(nil), policy.RequiredArtifactRoles...)
	sort.Strings(roles)
	bindingDigest, err := canonicalRecoveryBindingDigest(normalized, unsignedDigest, roles)
	if err != nil {
		return model.RecoveryPoint{}, err
	}
	normalized.BindingDigest = bindingDigest
	evidenceDigest, err := canonicalDigest(normalized)
	if err != nil {
		return model.RecoveryPoint{}, err
	}
	id, err := newUUID()
	if err != nil {
		return model.RecoveryPoint{}, err
	}
	recoverableUntil := now.Add(time.Duration(policy.RecoverableSeconds) * time.Second)
	point := model.RecoveryPoint{
		ID: id, TaskID: task.ID, Service: task.Service, TenantID: tenantID, ServerID: serverID,
		Status: "verified", Evidence: normalized, EvidenceDigest: evidenceDigest,
		ExpectedBeforeDigest: expectedBeforeDigest, ExpectedBefore: cloneMap(task.Snapshot),
		BindingDigest: bindingDigest, RequiredArtifactRoles: roles,
		CreatedAt: evidence.CreatedAt, VerifiedAt: &now, RecoverableUntil: &recoverableUntil,
	}
	if err := engine.store.SaveRecoveryPoint(ctx, point); err != nil {
		return model.RecoveryPoint{}, err
	}
	return point, nil
}

func (engine *Engine) verifyRecoveryPoint(
	ctx context.Context,
	task model.Task,
	service model.ServiceDefinition,
	pointID string,
) error {
	if pointID == "" || service.RecoveryPointPolicy == nil {
		return errors.New("变更阶段缺少已验证恢复点")
	}
	point, err := engine.store.GetRecoveryPoint(ctx, pointID)
	if err != nil {
		return fmt.Errorf("读取恢复点: %w", err)
	}
	if point.Status != "verified" || point.TaskID != task.ID || point.Service != task.Service {
		return errors.New("恢复点状态或身份与当前任务不匹配")
	}
	now := time.Now().UTC()
	if point.RecoverableUntil == nil || !point.RecoverableUntil.After(now) {
		return errors.New("恢复点已过期")
	}
	expectedBeforeDigest, err := canonicalDigest(task.Snapshot)
	if err != nil || point.ExpectedBeforeDigest != expectedBeforeDigest {
		return errors.New("恢复点未绑定当前变更前身份")
	}
	if !sameStringSet(point.RequiredArtifactRoles, service.RecoveryPointPolicy.RequiredArtifactRoles) {
		return errors.New("恢复点必需角色策略已经变化")
	}
	tenantID := service.TenantID
	if tenantID == "" && engine.catalog.Access != nil {
		tenantID = engine.catalog.Access.DefaultTenant
	}
	if tenantID == "" {
		tenantID = "default"
	}
	if point.TenantID != "" && point.TenantID != tenantID {
		return errors.New("恢复点租户与当前服务不一致")
	}
	if point.ServerID != "" && point.ServerID != service.ServerID {
		return errors.New("恢复点服务器与当前服务不一致")
	}
	if point.Evidence.TenantID != "" && point.Evidence.TenantID != tenantID {
		return errors.New("恢复点证据租户与当前服务不一致")
	}
	if point.Evidence.ServerID != "" && point.Evidence.ServerID != service.ServerID {
		return errors.New("恢复点证据服务器与当前服务不一致")
	}
	if point.Evidence.ExpectedBeforeDigest != "" && point.Evidence.ExpectedBeforeDigest != expectedBeforeDigest {
		return errors.New("恢复点证据未绑定当前变更前身份")
	}
	// New points carry a binding digest. Legacy points are still readable, but
	// any point that claims a binding must pass both the binding and final
	// envelope digest checks before artifacts are touched.
	if point.BindingDigest != "" || point.Evidence.BindingDigest != "" {
		unsigned := point.Evidence
		unsigned.BindingDigest = ""
		unsignedDigest, digestErr := canonicalDigest(unsigned)
		if digestErr != nil {
			return digestErr
		}
		roles := append([]string(nil), point.RequiredArtifactRoles...)
		sort.Strings(roles)
		bindingDigest, digestErr := canonicalRecoveryBindingDigest(point.Evidence, unsignedDigest, roles)
		if digestErr != nil || bindingDigest != point.BindingDigest || bindingDigest != point.Evidence.BindingDigest {
			return errors.New("恢复点绑定摘要不匹配")
		}
	}
	evidenceDigest, err := canonicalDigest(point.Evidence)
	if err != nil {
		return err
	}
	if point.EvidenceDigest != evidenceDigest {
		// Keep the failure classified as an artifact/evidence failure. Callers
		// use this boundary to distinguish a tampered backup from a stale plan.
		return errors.New("恢复点产物证据摘要不匹配")
	}
	return engine.validateRecoveryArtifacts(point.Evidence, point.RequiredArtifactRoles)
}

// verifyRestoreTaskBinding validates a production/isolated restore task against
// the point selected during approval. Restore points are created by an earlier
// backup task, so the generic update gate (which requires point.TaskID to equal
// the current task) is intentionally not used here.
func (engine *Engine) verifyRestoreTaskBinding(
	ctx context.Context, task model.Task, service model.ServiceDefinition, pointID string,
) (model.RecoveryPoint, error) {
	if pointID == "" || task.RestoreMode == "" {
		return model.RecoveryPoint{}, errors.New("恢复任务缺少恢复点绑定")
	}
	point, err := engine.store.GetRecoveryPoint(ctx, pointID)
	if err != nil {
		return model.RecoveryPoint{}, fmt.Errorf("读取恢复点: %w", err)
	}
	if point.Status != "verified" || point.Service != task.Service {
		return model.RecoveryPoint{}, errors.New("恢复点状态或服务身份不匹配")
	}
	if point.RecoverableUntil == nil || !point.RecoverableUntil.After(time.Now().UTC()) {
		return model.RecoveryPoint{}, errors.New("恢复点已过期")
	}
	if task.RestoreTenantID != point.TenantID || task.RestoreServerID != point.ServerID ||
		task.RestoreExpectedBeforeDigest != point.ExpectedBeforeDigest ||
		task.RestoreContractDigest != point.BindingDigest ||
		task.RestoreEvidenceDigest != point.EvidenceDigest {
		return model.RecoveryPoint{}, errors.New("恢复任务绑定与恢复点不一致")
	}
	// Reuse the artifact, tenant, server, and digest checks, but use the point's
	// immutable expected-before snapshot rather than the restore task snapshot.
	validationTask := model.Task{ID: point.TaskID, Service: point.Service, Snapshot: cloneMap(point.ExpectedBefore)}
	if err := engine.verifyRecoveryPoint(ctx, validationTask, service, point.ID); err != nil {
		return model.RecoveryPoint{}, err
	}
	return point, nil
}

// verifyRestorePlanBinding re-reads the selected point immediately before a
// production task is created. Approval summaries are immutable, but the point
// can expire or be marked invalid while an operator is reviewing the plan.
func (engine *Engine) verifyRestorePlanBinding(
	ctx context.Context, plan model.ReleasePlan, service model.ServiceDefinition,
) (model.RecoveryPoint, error) {
	if plan.RecoveryPointID == "" || plan.RestoreMode == "" {
		return model.RecoveryPoint{}, errors.New("恢复计划缺少恢复点绑定")
	}
	point, err := engine.store.GetRecoveryPoint(ctx, plan.RecoveryPointID)
	if err != nil {
		return model.RecoveryPoint{}, fmt.Errorf("读取恢复点: %w", err)
	}
	synthetic := model.Task{
		ID: point.TaskID, Service: service.Name,
		Snapshot: cloneMap(point.ExpectedBefore),
	}
	if synthetic.Snapshot == nil {
		synthetic.Snapshot = cloneMap(plan.ApprovalSummary.ExpectedBefore)
	}
	if err := engine.verifyRecoveryPoint(ctx, synthetic, service, point.ID); err != nil {
		return model.RecoveryPoint{}, err
	}
	if plan.RestoreTenantID != point.TenantID || plan.RestoreServerID != point.ServerID ||
		plan.RestoreExpectedBeforeDigest != point.ExpectedBeforeDigest ||
		plan.RestoreContractDigest != point.BindingDigest ||
		plan.RestoreEvidenceDigest != point.EvidenceDigest {
		return model.RecoveryPoint{}, errors.New("恢复计划绑定已变化，请重新创建计划")
	}
	if plan.ApprovalSummary.RecoveryPointBindingDigest != point.BindingDigest ||
		plan.ApprovalSummary.RecoveryPointEvidenceDigest != point.EvidenceDigest {
		return model.RecoveryPoint{}, errors.New("恢复批准摘要与恢复点绑定不一致")
	}
	return point, nil
}

type recoveryBindingEnvelope struct {
	SchemaVersion        int      `json:"schemaVersion"`
	Service              string   `json:"service"`
	TaskID               string   `json:"taskId"`
	TenantID             string   `json:"tenantId"`
	ServerID             string   `json:"serverId"`
	ExpectedBeforeDigest string   `json:"expectedBeforeDigest"`
	EvidenceDigest       string   `json:"evidenceDigest"`
	RequiredRoles        []string `json:"requiredRoles"`
}

type restorePointContract struct {
	SchemaVersion        int                 `json:"schemaVersion"`
	TaskID               string              `json:"taskId"`
	PlanID               string              `json:"planId"`
	Service              string              `json:"service"`
	Mode                 string              `json:"mode"`
	TenantID             string              `json:"tenantId"`
	ServerID             string              `json:"serverId"`
	RecoveryPointID      string              `json:"recoveryPointId"`
	BindingDigest        string              `json:"bindingDigest"`
	EvidenceDigest       string              `json:"evidenceDigest"`
	ExpectedBeforeDigest string              `json:"expectedBeforeDigest"`
	RevalidatedAt        time.Time           `json:"revalidatedAt"`
	RecoveryPoint        model.RecoveryPoint `json:"recoveryPoint"`
}

func writeRestorePointContract(
	operationDir string, task model.Task, point model.RecoveryPoint,
) (string, error) {
	if task.RestoreRevalidatedAt == nil || task.RestoreMode == "" || task.PlanID == "" {
		return "", errors.New("恢复任务缺少执行前复验信息")
	}
	contract := restorePointContract{
		SchemaVersion: 1, TaskID: task.ID, PlanID: task.PlanID, Service: task.Service,
		Mode: task.RestoreMode, TenantID: task.RestoreTenantID, ServerID: task.RestoreServerID,
		RecoveryPointID: point.ID, BindingDigest: task.RestoreContractDigest,
		EvidenceDigest:       task.RestoreEvidenceDigest,
		ExpectedBeforeDigest: task.RestoreExpectedBeforeDigest,
		RevalidatedAt:        task.RestoreRevalidatedAt.UTC(), RecoveryPoint: point,
	}
	if contract.RecoveryPointID != task.RecoveryPointID || contract.BindingDigest != point.BindingDigest ||
		contract.EvidenceDigest != point.EvidenceDigest || contract.ExpectedBeforeDigest != point.ExpectedBeforeDigest {
		return "", errors.New("恢复合同与已验证恢复点不一致")
	}
	data, err := json.Marshal(contract)
	if err != nil {
		return "", err
	}
	path := filepath.Join(operationDir, "recovery-point.json")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("创建恢复点合同失败: %w", err)
	}
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", closeErr
	}
	digest := sha256.Sum256(append(data, '\n'))
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func verifyRestorePointContract(operationDir, expectedDigest string) error {
	path := filepath.Join(operationDir, "recovery-point.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return errors.New("恢复点合同缺失、类型或权限不安全")
	}
	digest, err := fileSHA256(path)
	if err != nil || expectedDigest != "sha256:"+digest {
		return errors.New("恢复点合同在执行期间发生变化")
	}
	return nil
}

func canonicalRecoveryBindingDigest(
	evidence model.RecoveryPointEvidence,
	unsignedDigest string,
	requiredRoles []string,
) (string, error) {
	return canonicalDigest(recoveryBindingEnvelope{
		SchemaVersion: evidence.SchemaVersion, Service: evidence.Service, TaskID: evidence.TaskID,
		TenantID: evidence.TenantID, ServerID: evidence.ServerID,
		ExpectedBeforeDigest: evidence.ExpectedBeforeDigest, EvidenceDigest: unsignedDigest,
		RequiredRoles: requiredRoles,
	})
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	data, err := json.Marshal(input)
	if err != nil {
		return nil
	}
	var output map[string]any
	if json.Unmarshal(data, &output) != nil {
		return nil
	}
	return output
}

func (engine *Engine) validateRecoveryArtifacts(
	evidence model.RecoveryPointEvidence,
	requiredRoles []string,
) error {
	if len(evidence.Artifacts) == 0 || len(evidence.Artifacts) > 16 {
		return errors.New("恢复点产物数量无效")
	}
	roles := make(map[string]struct{}, len(evidence.Artifacts))
	root := filepath.Clean(engine.backupRoot)
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return errors.New("受控备份目录不可用")
	}
	for _, artifact := range evidence.Artifacts {
		if artifact.Role == "" {
			return errors.New("恢复点产物角色为空")
		}
		if _, exists := roles[artifact.Role]; exists {
			return fmt.Errorf("恢复点产物角色重复: %s", artifact.Role)
		}
		roles[artifact.Role] = struct{}{}
		clean := filepath.Clean(artifact.Path)
		resolved, err := filepath.EvalSymlinks(clean)
		if err != nil || !strings.HasPrefix(resolved, resolvedRoot+string(os.PathSeparator)) {
			return errors.New("恢复点产物不在受控备份目录")
		}
		info, err := os.Lstat(clean)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != artifact.SizeBytes {
			return fmt.Errorf("恢复点产物无效: %s", artifact.Role)
		}
		if info.ModTime().Before(evidence.CreatedAt.Add(-30*time.Minute)) ||
			info.ModTime().After(evidence.CreatedAt.Add(time.Minute)) {
			return fmt.Errorf("恢复点产物时间与证据不匹配: %s", artifact.Role)
		}
		digest, err := fileSHA256(clean)
		if err != nil || artifact.SHA256 != "sha256:"+digest {
			return fmt.Errorf("恢复点产物摘要不匹配: %s", artifact.Role)
		}
	}
	for _, role := range requiredRoles {
		if _, exists := roles[role]; !exists {
			return fmt.Errorf("恢复点缺少必需产物角色: %s", role)
		}
	}
	return nil
}

func canonicalDigest(value any) (string, error) {
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]struct{}, len(left))
	for _, value := range left {
		values[value] = struct{}{}
	}
	for _, value := range right {
		if _, exists := values[value]; !exists {
			return false
		}
	}
	return true
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
