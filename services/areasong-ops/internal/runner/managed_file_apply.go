package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func (engine *Engine) ManagedFileProposals(ctx context.Context, actor string) ([]model.ManagedFileProposal, error) {
	if engine.catalog.Files == nil || !engine.catalog.Files.Enabled {
		return nil, errors.New("文件管理尚未启用")
	}
	items, err := engine.store.ListManagedFileProposals(ctx, 100)
	if err != nil {
		return nil, err
	}
	result := make([]model.ManagedFileProposal, 0, len(items))
	for _, item := range items {
		if engine.authorizeManagedFileRead(ctx, actor, item.RootID) != nil {
			continue
		}
		// Proposal content can contain configuration secrets. The list surface
		// exposes only identity and digests; the controlled file endpoint remains
		// the source for readable content.
		item.Content = ""
		result = append(result, item)
	}
	return result, nil
}

func (engine *Engine) ApproveManagedFileProposal(
	ctx context.Context, actor, id string, request model.ManagedFileApprovalRequest,
) (model.ManagedFileProposal, error) {
	if !actorPattern.MatchString(actor) || !uuidPattern.MatchString(id) {
		return model.ManagedFileProposal{}, errors.New("文件批准请求标识无效")
	}
	proposal, err := engine.store.GetManagedFileProposal(ctx, id)
	if err != nil {
		return model.ManagedFileProposal{}, err
	}
	if err := engine.authorize(ctx, actor, model.PermissionManageConfig, "file:"+proposal.RootID); err != nil {
		return model.ManagedFileProposal{}, err
	}
	approved, err := engine.store.ApproveManagedFileProposal(ctx, id, actor, request.Digest, request.Confirmation)
	if err != nil {
		return model.ManagedFileProposal{}, err
	}
	approved.Content = ""
	return approved, nil
}

func (engine *Engine) ApplyManagedFileProposal(
	ctx context.Context, actor, id string, request model.ManagedFileApplyRequest,
) (model.ManagedFileProposal, error) {
	if !actorPattern.MatchString(actor) || !uuidPattern.MatchString(id) || !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.ManagedFileProposal{}, errors.New("文件应用请求标识无效")
	}
	proposal, err := engine.store.GetManagedFileProposal(ctx, id)
	if err != nil {
		return model.ManagedFileProposal{}, err
	}
	if err := engine.authorize(ctx, actor, model.PermissionManageConfig, "file:"+proposal.RootID); err != nil {
		return model.ManagedFileProposal{}, err
	}
	proposal, started, err := engine.store.StartManagedFileApply(ctx, id, actor, request.IdempotencyKey)
	if err != nil {
		return proposal, err
	}
	if !started {
		proposal.Content = ""
		return proposal, nil
	}
	backupPath, changed, applyErr := engine.applyManagedFile(proposal)
	state, errorText := "applied", ""
	if applyErr != nil {
		state, errorText = "failed", redactText(applyErr.Error())
		if changed {
			state = "needs_attention"
		}
	}
	if finishErr := engine.store.FinishManagedFileApply(context.WithoutCancel(ctx), id, state, backupPath, errorText); finishErr != nil {
		return proposal, fmt.Errorf("文件应用状态收口失败: %w", finishErr)
	}
	finished := time.Now().UTC()
	proposal.State, proposal.BackupPath, proposal.Error, proposal.FinishedAt = state, backupPath, errorText, &finished
	proposal.Content = ""
	if applyErr != nil {
		return proposal, applyErr
	}
	return proposal, nil
}

func (engine *Engine) RollbackManagedFileProposal(
	ctx context.Context, actor, id string, request model.ManagedFileRollbackRequest,
) (model.ManagedFileProposal, error) {
	if !actorPattern.MatchString(actor) || !uuidPattern.MatchString(id) || !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.ManagedFileProposal{}, errors.New("文件回滚请求标识无效")
	}
	proposal, err := engine.store.GetManagedFileProposal(ctx, id)
	if err != nil {
		return model.ManagedFileProposal{}, err
	}
	if err := engine.authorize(ctx, actor, model.PermissionManageConfig, "file:"+proposal.RootID); err != nil {
		return model.ManagedFileProposal{}, err
	}
	if request.Confirmation != "回滚文件变更 "+id {
		return model.ManagedFileProposal{}, errors.New("文件回滚确认短语不匹配")
	}
	proposal, started, err := engine.store.StartManagedFileRollback(ctx, id, actor, request.IdempotencyKey)
	if err != nil {
		return proposal, err
	}
	if !started {
		proposal.Content = ""
		return proposal, nil
	}
	changed, rollbackErr := engine.rollbackManagedFile(proposal)
	state, errorText := "rolled_back", ""
	if rollbackErr != nil {
		state, errorText = "needs_attention", redactText(rollbackErr.Error())
		if !changed {
			state = "failed"
		}
	}
	if state == "failed" {
		// A rollback that did not touch the file is still retained as an
		// attention item; automatic retries are not allowed for configuration.
		state = "needs_attention"
	}
	if finishErr := engine.store.FinishManagedFileRollback(context.WithoutCancel(ctx), id, state, errorText); finishErr != nil {
		return proposal, fmt.Errorf("文件回滚状态收口失败: %w", finishErr)
	}
	finished := time.Now().UTC()
	proposal.State, proposal.Error, proposal.RolledBackAt, proposal.FinishedAt = state, errorText, &finished, &finished
	proposal.Content = ""
	if rollbackErr != nil {
		return proposal, rollbackErr
	}
	return proposal, nil
}

func (engine *Engine) applyManagedFile(proposal model.ManagedFileProposal) (string, bool, error) {
	root, _, cleanPath, err := engine.resolveManagedPath(proposal.RootID, proposal.Path)
	if err != nil {
		return "", false, err
	}
	current, before, err := openManagedNode(root, cleanPath)
	if err != nil {
		return "", false, err
	}
	defer current.Close()
	if !before.Mode().IsRegular() {
		return "", false, errors.New("文件目标身份无效")
	}
	currentContent, err := readManagedTextFile(current, engine.catalog.Files.MaxFileBytes)
	if err != nil {
		return "", false, err
	}
	if digestText(currentContent) != proposal.ExpectedDigest {
		return "", false, errors.New("文件基线摘要在执行前已变化")
	}
	backupPath, err := engine.writeManagedFileBackup(proposal, currentContent)
	if err != nil {
		return "", false, err
	}
	changed, err := replaceManagedFile(
		root, cleanPath, proposal.Content, proposal.ExpectedDigest, proposal.ProposedDigest, before,
	)
	return backupPath, changed, err
}

func (engine *Engine) rollbackManagedFile(proposal model.ManagedFileProposal) (bool, error) {
	root, _, cleanPath, err := engine.resolveManagedPath(proposal.RootID, proposal.Path)
	if err != nil {
		return false, err
	}
	currentFile, current, err := openManagedNode(root, cleanPath)
	if err != nil || !current.Mode().IsRegular() {
		return false, errors.New("文件回滚目标身份无效")
	}
	defer currentFile.Close()
	content, err := readManagedTextFile(currentFile, engine.catalog.Files.MaxFileBytes)
	if err != nil || digestText(content) != proposal.ProposedDigest {
		return false, errors.New("当前文件不再对应已应用提案")
	}
	expectedBackupPath := filepath.Join(engine.stateRoot, "file-backups", proposal.ID, "before")
	if filepath.Clean(proposal.BackupPath) != filepath.Clean(expectedBackupPath) {
		return false, errors.New("文件回滚副本不可用")
	}
	backup, err := readManagedBackup(proposal.BackupPath, engine.catalog.Files.MaxFileBytes)
	if err != nil || digestText(backup) != proposal.ExpectedDigest {
		return false, errors.New("文件回滚副本摘要不匹配")
	}
	return replaceManagedFile(
		root, cleanPath, backup, proposal.ProposedDigest, proposal.ExpectedDigest, current,
	)
}

func (engine *Engine) writeManagedFileBackup(proposal model.ManagedFileProposal, content string) (string, error) {
	directory := filepath.Join(engine.stateRoot, "file-backups", proposal.ID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(directory, "before")
	destination, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, copyErr := io.WriteString(destination, content)
	syncErr := destination.Sync()
	closeErr := destination.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if syncErr != nil {
		return "", syncErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	backup, err := os.ReadFile(path)
	if err != nil || digestText(string(backup)) != proposal.ExpectedDigest {
		return "", errors.New("文件备份摘要核验失败")
	}
	return path, managedSyncDirectory(directory)
}

func managedSyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
