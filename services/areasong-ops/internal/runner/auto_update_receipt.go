package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

const automaticRollbackReceiptName = "automatic-rollback-result.json"

type automaticRollbackReceipt struct {
	Version     int             `json:"version"`
	TaskID      string          `json:"taskId"`
	PlanID      string          `json:"planId"`
	PlanDigest  string          `json:"planDigest"`
	State       model.TaskState `json:"state"`
	Summary     string          `json:"summary"`
	Error       string          `json:"error,omitempty"`
	FailureCode string          `json:"failureCode"`
	CompletedAt time.Time       `json:"completedAt"`
}

func (engine *Engine) finishAutomaticRollback(task model.Task, state model.TaskState, summary, failure, code string) error {
	receipt := automaticRollbackReceipt{Version: 1, TaskID: task.ID, PlanID: task.PlanID, PlanDigest: task.PlanDigest,
		State: state, Summary: redactText(summary), Error: redactText(failure), FailureCode: code, CompletedAt: time.Now().UTC()}
	directory := filepath.Join(engine.stateRoot, "operations", task.ID)
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".automatic-rollback-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	// 同目录硬链接原子发布完整回执，已有回执不能被覆盖。
	if err := os.Link(file.Name(), filepath.Join(directory, automaticRollbackReceiptName)); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr = dir.Close()
	if syncErr != nil || closeErr != nil {
		return errors.Join(syncErr, closeErr)
	}
	return engine.persistAutomaticRollbackReceipt(task, receipt)
}

func (engine *Engine) persistAutomaticRollbackReceipt(task model.Task, receipt automaticRollbackReceipt) error {
	err := engine.completeTask(task, receipt.State, receipt.Summary, receipt.Error, receipt.FailureCode, false, false, "")
	if err == nil {
		return nil
	}
	stored, readErr := engine.store.GetTask(context.Background(), task.ID)
	if readErr == nil && stored.State == receipt.State && stored.PlanDigest == receipt.PlanDigest {
		return nil
	}
	return fmt.Errorf("回滚动作已结束但终态未提交；仅允许重试回执持久化: %w", err)
}

func (engine *Engine) reconcileAutomaticRollbackReceipts(ctx context.Context) error {
	_, err := engine.recoverAutomaticRollbackReceipts(ctx)
	return err
}

// 先补交已完成回执，再分类中断任务；这里不启动执行器或后台协调器。
func RecoverAutomaticRollbackReceipts(ctx context.Context, catalog *config.Catalog, database *store.Store, root string, manager Alertmanager) (int, error) {
	if manager == nil {
		manager = unavailableAlertmanager{}
	}
	finisher := &Engine{catalog: catalog, store: database, stateRoot: root, broker: NewBroker(), alertmanager: manager}
	return finisher.recoverAutomaticRollbackReceipts(ctx)
}

func (engine *Engine) recoverAutomaticRollbackReceipts(ctx context.Context) (int, error) {
	tasks, err := engine.store.AutomaticRollbackPendingTasks(ctx)
	if err != nil {
		return 0, err
	}
	completed := 0
	for _, task := range tasks {
		if !uuidPattern.MatchString(task.ID) {
			return completed, errors.New("自动回滚任务标识无效")
		}
		plan, err := engine.store.GetReleasePlan(ctx, task.PlanID)
		if err != nil || task.Action != "update" || plan.Digest != task.PlanDigest || plan.ApprovalSummary.AutoUpdatePolicy == nil ||
			!plan.ApprovalSummary.AutoUpdatePolicy.RollbackOnAlert || !plan.AllowsExecutor(plan.ExecutedByHash) {
			return completed, errors.New("自动回滚回执缺少原始批准关系")
		}
		path := filepath.Join(engine.stateRoot, "operations", task.ID, automaticRollbackReceiptName)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 32768 {
			return completed, errors.New("自动回滚回执缺失或权限不安全")
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != uint32(os.Geteuid()) {
			return completed, errors.New("自动回滚回执属主不安全")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return completed, err
		}
		var receipt automaticRollbackReceipt
		if err := json.Unmarshal(data, &receipt); err != nil {
			return completed, err
		}
		if receipt.Version != 1 || receipt.TaskID != task.ID || receipt.PlanID != task.PlanID || receipt.PlanDigest != task.PlanDigest ||
			(receipt.State != model.TaskRolledBack && receipt.State != model.TaskNeedsAttention) {
			return completed, errors.New("自动回滚回执身份或终态不一致")
		}
		if err := engine.persistAutomaticRollbackReceipt(task, receipt); err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
}
