package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
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
	_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{ActorHash: actor, Event: "file.proposal.approved", Resource: id, Outcome: approved.State, Detail: map[string]any{"rootId": approved.RootID, "path": approved.Path, "digest": approved.ProposedDigest}})
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
	_, _ = engine.store.AppendAudit(context.Background(), model.AuditEntry{ActorHash: actor, Event: "file.proposal.applied", Resource: id, Outcome: state, Detail: map[string]any{"rootId": proposal.RootID, "path": proposal.Path, "digest": proposal.ProposedDigest, "changed": changed}})
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
	_, _ = engine.store.AppendAudit(context.Background(), model.AuditEntry{ActorHash: actor, Event: "file.proposal.rolled_back", Resource: id, Outcome: state, Detail: map[string]any{"rootId": proposal.RootID, "path": proposal.Path, "changed": changed}})
	if rollbackErr != nil {
		return proposal, rollbackErr
	}
	return proposal, nil
}

func (engine *Engine) applyManagedFile(proposal model.ManagedFileProposal) (string, bool, error) {
	_, target, _, err := engine.resolveManagedPath(proposal.RootID, proposal.Path)
	if err != nil {
		return "", false, err
	}
	currentContent, err := readManagedText(target, engine.catalog.Files.MaxFileBytes)
	if err != nil {
		return "", false, err
	}
	if digestText(currentContent) != proposal.ExpectedDigest {
		return "", false, errors.New("文件基线摘要在执行前已变化")
	}
	before, err := os.Lstat(target)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return "", false, errors.New("文件目标身份无效")
	}
	backupPath, err := engine.writeManagedFileBackup(proposal, target, before)
	if err != nil {
		return "", false, err
	}
	temporary, err := writeManagedReplacement(target, proposal.Content, before)
	if err != nil {
		return backupPath, false, err
	}
	defer os.Remove(temporary)
	if err := verifyManagedTargetUnchanged(target, before, proposal.ExpectedDigest); err != nil {
		return backupPath, false, err
	}
	if err := os.Rename(temporary, target); err != nil {
		return backupPath, false, err
	}
	if err := managedSyncDirectory(filepath.Dir(target)); err != nil {
		return backupPath, true, err
	}
	content, err := os.ReadFile(target)
	if err != nil || digestText(string(content)) != proposal.ProposedDigest {
		return backupPath, true, errors.New("文件原子替换后的摘要核验失败")
	}
	return backupPath, true, nil
}

func (engine *Engine) rollbackManagedFile(proposal model.ManagedFileProposal) (bool, error) {
	_, target, _, err := engine.resolveManagedPath(proposal.RootID, proposal.Path)
	if err != nil {
		return false, err
	}
	current, err := os.Lstat(target)
	if err != nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("文件回滚目标身份无效")
	}
	content, err := os.ReadFile(target)
	if err != nil || digestText(string(content)) != proposal.ProposedDigest {
		return false, errors.New("当前文件不再对应已应用提案")
	}
	backupInfo, err := os.Lstat(proposal.BackupPath)
	if err != nil || !backupInfo.Mode().IsRegular() || backupInfo.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("文件回滚副本不可用")
	}
	backup, err := os.ReadFile(proposal.BackupPath)
	if err != nil || digestText(string(backup)) != proposal.ExpectedDigest {
		return false, errors.New("文件回滚副本摘要不匹配")
	}
	temporary, err := writeManagedReplacement(target, string(backup), current)
	if err != nil {
		return false, err
	}
	defer os.Remove(temporary)
	if err := verifyManagedTargetUnchanged(target, current, proposal.ProposedDigest); err != nil {
		return false, err
	}
	if err := os.Rename(temporary, target); err != nil {
		return false, err
	}
	if err := managedSyncDirectory(filepath.Dir(target)); err != nil {
		return true, err
	}
	final, err := os.ReadFile(target)
	if err != nil || digestText(string(final)) != proposal.ExpectedDigest {
		return true, errors.New("文件回滚后的摘要核验失败")
	}
	return true, nil
}

func (engine *Engine) writeManagedFileBackup(proposal model.ManagedFileProposal, target string, info os.FileInfo) (string, error) {
	directory := filepath.Join(engine.stateRoot, "file-backups", proposal.ID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(directory, "before")
	source, err := os.Open(target)
	if err != nil {
		return "", err
	}
	defer source.Close()
	destination, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(destination, source)
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
	_ = info
	return path, managedSyncDirectory(directory)
}

func writeManagedReplacement(target, content string, original os.FileInfo) (string, error) {
	directory := filepath.Dir(target)
	file, err := os.CreateTemp(directory, ".areasong-ops-file-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	clean := func(result error) (string, error) {
		_ = file.Close()
		_ = os.Remove(path)
		return "", result
	}
	if err := file.Chmod(original.Mode().Perm()); err != nil {
		return clean(err)
	}
	if stat, ok := original.Sys().(*syscall.Stat_t); ok {
		if err := file.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
			return clean(err)
		}
	}
	if _, err := file.WriteString(content); err != nil {
		return clean(err)
	}
	if err := file.Sync(); err != nil {
		return clean(err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func verifyManagedTargetUnchanged(target string, expected os.FileInfo, digest string) error {
	current, err := os.Lstat(target)
	if err != nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 {
		return errors.New("文件目标在替换前身份失效")
	}
	expectedStat, expectedOK := expected.Sys().(*syscall.Stat_t)
	currentStat, currentOK := current.Sys().(*syscall.Stat_t)
	if expectedOK && currentOK && (expectedStat.Dev != currentStat.Dev || expectedStat.Ino != currentStat.Ino) {
		return errors.New("文件目标在替换前 inode 已变化")
	}
	content, err := os.ReadFile(target)
	if err != nil || digestText(string(content)) != digest {
		return errors.New("文件目标在替换前摘要已变化")
	}
	return nil
}

func managedSyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
