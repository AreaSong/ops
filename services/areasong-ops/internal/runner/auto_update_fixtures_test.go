package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

type automaticTestExecutor struct {
	fakeExecutor
	stateMu         sync.Mutex
	backupRoot      string
	updated         bool
	missingBackup   bool
	healthFailed    bool
	rollbackEntered chan struct{}
}

func (executor *automaticTestExecutor) Execute(ctx context.Context, input ExecuteInput) (model.AdapterResult, error) {
	result, err := executor.fakeExecutor.Execute(ctx, input)
	if err != nil {
		return result, err
	}
	if input.Phase == "rollback" && executor.rollbackEntered != nil {
		close(executor.rollbackEntered)
		<-ctx.Done()
		return result, ctx.Err()
	}
	executor.stateMu.Lock()
	defer executor.stateMu.Unlock()
	if input.Action == "inspect" && executor.updated {
		if input.Phase == "inspect" && executor.healthFailed {
			return result, errors.New("应用健康端点不可用")
		}
		result.Data["currentVersion"] = "1.1.0"
	}
	if input.Action == "update" && input.Phase == "apply" {
		executor.updated = true
	}
	if input.Phase == "rollback" {
		executor.updated = false
	}
	if input.Phase != "backup" || executor.missingBackup {
		return result, nil
	}
	content := []byte("isolated-backup")
	path := filepath.Join(executor.backupRoot, filepath.Base(input.OperationDir)+".backup")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return result, err
	}
	digest := sha256.Sum256(content)
	result.RecoveryPoint = &model.RecoveryPointEvidence{SchemaVersion: 1,
		Service: input.Service.Name, TaskID: filepath.Base(input.OperationDir), CreatedAt: time.Now().UTC(),
		Artifacts: []model.RecoveryArtifact{{Role: "backup-demo", Path: path,
			SizeBytes: int64(len(content)), SHA256: "sha256:" + hex.EncodeToString(digest[:])}},
	}
	return result, nil
}

func automaticTestEngine(t *testing.T) (*Engine, *store.Store) {
	t.Helper()
	executor := &automaticTestExecutor{backupRoot: t.TempDir()}
	engine, database := testEngine(t, executor)
	engine.backupRoot = executor.backupRoot
	service := engine.catalog.Services["demo"]
	service.RecoveryPointPolicy = &model.RecoveryPointPolicy{RequiredArtifactRoles: []string{"backup-demo"}, RecoverableSeconds: 86400}
	action := service.Actions["update"]
	action.PhaseSemantics = map[string]model.PhaseSemantics{
		"backup":   {Effect: "artifact_write", ProducesRecoveryPoint: true, FailurePolicy: "fail"},
		"apply":    {Effect: "runtime_mutation", RequiresRecoveryPoint: true, FailurePolicy: "rollback", RecoveryPhase: "rollback"},
		"health":   {Effect: "observe", RequiresRecoveryPoint: true, FailurePolicy: "rollback", RecoveryPhase: "rollback"},
		"identity": {Effect: "observe", RequiresRecoveryPoint: true, FailurePolicy: "rollback", RecoveryPhase: "rollback"},
	}
	service.Actions["update"] = action
	engine.catalog.Services["demo"] = service
	return engine, database
}
