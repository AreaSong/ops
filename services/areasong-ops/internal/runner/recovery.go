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
	if semantics, ok := action.PhaseSemantics[phase]; ok {
		return semantics
	}
	semantics := model.PhaseSemantics{Effect: "observe", FailurePolicy: "fail"}
	switch phase {
	case "backup":
		semantics.Effect = "artifact_write"
	case "migration":
		semantics.Effect = "data_mutation"
		semantics.FailurePolicy = "needs_attention"
	case "apply", "restart":
		semantics.Effect = "runtime_mutation"
		if action.Name == "update" {
			semantics.FailurePolicy = "rollback"
			semantics.RecoveryPhase = "rollback"
		}
	}
	return semantics
}

func mutationSemantics(semantics model.PhaseSemantics) bool {
	return semantics.Effect == "runtime_mutation" || semantics.Effect == "data_mutation"
}

func (engine *Engine) persistRecoveryPoint(
	ctx context.Context,
	task model.Task,
	evidence *model.RecoveryPointEvidence,
) (model.RecoveryPoint, error) {
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
	if len(evidence.Artifacts) == 0 || len(evidence.Artifacts) > 16 {
		return model.RecoveryPoint{}, errors.New("恢复点产物数量无效")
	}
	roles := make(map[string]struct{}, len(evidence.Artifacts))
	root := filepath.Clean(engine.backupRoot)
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return model.RecoveryPoint{}, errors.New("受控备份目录不可用")
	}
	for _, artifact := range evidence.Artifacts {
		if artifact.Role == "" {
			return model.RecoveryPoint{}, errors.New("恢复点产物角色为空")
		}
		if _, exists := roles[artifact.Role]; exists {
			return model.RecoveryPoint{}, fmt.Errorf("恢复点产物角色重复: %s", artifact.Role)
		}
		roles[artifact.Role] = struct{}{}
		clean := filepath.Clean(artifact.Path)
		resolved, err := filepath.EvalSymlinks(clean)
		if err != nil || !strings.HasPrefix(resolved, resolvedRoot+string(os.PathSeparator)) {
			return model.RecoveryPoint{}, errors.New("恢复点产物不在受控备份目录")
		}
		info, err := os.Lstat(clean)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != artifact.SizeBytes {
			return model.RecoveryPoint{}, fmt.Errorf("恢复点产物无效: %s", artifact.Role)
		}
		if info.ModTime().Before(now.Add(-30*time.Minute)) || info.ModTime().After(now.Add(time.Minute)) {
			return model.RecoveryPoint{}, fmt.Errorf("恢复点产物不新鲜: %s", artifact.Role)
		}
		digest, err := fileSHA256(clean)
		if err != nil || artifact.SHA256 != "sha256:"+digest {
			return model.RecoveryPoint{}, fmt.Errorf("恢复点产物摘要不匹配: %s", artifact.Role)
		}
	}
	canonical, err := json.Marshal(evidence)
	if err != nil {
		return model.RecoveryPoint{}, err
	}
	digest := sha256.Sum256(canonical)
	id, err := newUUID()
	if err != nil {
		return model.RecoveryPoint{}, err
	}
	recoverableUntil := now.Add(7 * 24 * time.Hour)
	point := model.RecoveryPoint{
		ID: id, TaskID: task.ID, Service: task.Service, Status: "verified", Evidence: *evidence,
		EvidenceDigest: "sha256:" + hex.EncodeToString(digest[:]), CreatedAt: evidence.CreatedAt,
		VerifiedAt: &now, RecoverableUntil: &recoverableUntil,
	}
	if err := engine.store.SaveRecoveryPoint(ctx, point); err != nil {
		return model.RecoveryPoint{}, err
	}
	return point, nil
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
