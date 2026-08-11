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
	canonical, err := json.Marshal(evidence)
	if err != nil {
		return model.RecoveryPoint{}, err
	}
	digest := sha256.Sum256(canonical)
	expectedBeforeDigest, err := canonicalDigest(task.Snapshot)
	if err != nil {
		return model.RecoveryPoint{}, err
	}
	id, err := newUUID()
	if err != nil {
		return model.RecoveryPoint{}, err
	}
	recoverableUntil := now.Add(time.Duration(policy.RecoverableSeconds) * time.Second)
	point := model.RecoveryPoint{
		ID: id, TaskID: task.ID, Service: task.Service, Status: "verified", Evidence: *evidence,
		EvidenceDigest:        "sha256:" + hex.EncodeToString(digest[:]),
		ExpectedBeforeDigest:  expectedBeforeDigest,
		RequiredArtifactRoles: append([]string(nil), policy.RequiredArtifactRoles...),
		CreatedAt:             evidence.CreatedAt, VerifiedAt: &now, RecoverableUntil: &recoverableUntil,
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
	canonical, err := json.Marshal(point.Evidence)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonical)
	if point.EvidenceDigest != "sha256:"+hex.EncodeToString(digest[:]) {
		return errors.New("恢复点证据摘要不匹配")
	}
	return engine.validateRecoveryArtifacts(point.Evidence, point.RequiredArtifactRoles)
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
