package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type Metrics struct {
	TasksByState      map[model.TaskState]int64
	TasksByService    []TaskMetric
	LastFinishedTasks []FinishedTaskMetric
	OldestActiveAge   float64
	LastSnapshotEpoch float64
}

type TaskMetric struct {
	Service string
	Action  string
	State   model.TaskState
	Count   int64
}

type FinishedTaskMetric struct {
	Service       string
	Action        string
	State         model.TaskState
	FinishedEpoch float64
}

func (store *Store) RecoverInterrupted(ctx context.Context) (int64, error) {
	now := timeText(store.now())
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	safe, err := tx.ExecContext(ctx, `
		UPDATE tasks SET state = ?, finished_at = ?, error = ?, summary = ?, retryable = 1,
			failure_code = 'runner_interrupted', heartbeat_at = NULL
		WHERE state = ? OR (state = ? AND production_changed = 0
			AND current_phase NOT IN ('migration', 'apply', 'restart'))
	`, model.TaskFailedRecoverable, now,
		"Runner 在任务完成前重启，尚无生产变更证据，可重新创建计划后执行",
		"Runner 重启，任务可恢复", model.TaskQueued, model.TaskRunning)
	if err != nil {
		return 0, err
	}
	unsafe, err := tx.ExecContext(ctx, `
		UPDATE tasks SET state = ?, finished_at = ?, error = ?, summary = ?, retryable = 0,
			failure_code = 'runner_interrupted_after_change', rollback_available = (action = 'update'),
			rollback_reason = 'Runner 中断时生产可能已改变，必须人工核对', heartbeat_at = NULL
		WHERE state IN (?, ?)
	`, model.TaskNeedsAttention, now,
		"Runner 在生产可能改变后重启，禁止自动处理，必须人工核对",
		"Runner 重启后等待人工核对", model.TaskRunning, model.TaskRollingBack)
	if err != nil {
		return 0, err
	}
	safeCount, err := safe.RowsAffected()
	if err != nil {
		return 0, err
	}
	unsafeCount, err := unsafe.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE release_plans SET state = ?, updated_at = ?
		WHERE state = ? AND task_id IN (SELECT id FROM tasks WHERE state IN (?, ?))
	`, model.PlanCompleted, now, model.PlanExecuting,
		model.TaskFailedRecoverable, model.TaskNeedsAttention); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return safeCount + unsafeCount, nil
}

func (store *Store) CollectMetrics(ctx context.Context) (Metrics, error) {
	metrics := Metrics{TasksByState: make(map[model.TaskState]int64)}
	rows, err := store.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM tasks GROUP BY state`)
	if err != nil {
		return metrics, err
	}
	for rows.Next() {
		var state model.TaskState
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			rows.Close()
			return metrics, err
		}
		metrics.TasksByState[state] = count
	}
	if err := rows.Close(); err != nil {
		return metrics, err
	}
	rows, err = store.db.QueryContext(ctx, `
		SELECT service, action, state, COUNT(*)
		FROM tasks
		GROUP BY service, action, state
		ORDER BY service, action, state
	`)
	if err != nil {
		return metrics, err
	}
	for rows.Next() {
		var item TaskMetric
		if err := rows.Scan(&item.Service, &item.Action, &item.State, &item.Count); err != nil {
			rows.Close()
			return metrics, err
		}
		metrics.TasksByService = append(metrics.TasksByService, item)
	}
	if err := rows.Close(); err != nil {
		return metrics, err
	}
	rows, err = store.db.QueryContext(ctx, `
		SELECT service, action, state, MAX(finished_at)
		FROM tasks
		WHERE finished_at IS NOT NULL
		GROUP BY service, action, state
		ORDER BY service, action, state
	`)
	if err != nil {
		return metrics, err
	}
	for rows.Next() {
		var item FinishedTaskMetric
		var finishedAt string
		if err := rows.Scan(&item.Service, &item.Action, &item.State, &finishedAt); err != nil {
			rows.Close()
			return metrics, err
		}
		finished, err := time.Parse(time.RFC3339Nano, finishedAt)
		if err != nil {
			rows.Close()
			return metrics, err
		}
		item.FinishedEpoch = float64(finished.Unix())
		metrics.LastFinishedTasks = append(metrics.LastFinishedTasks, item)
	}
	if err := rows.Close(); err != nil {
		return metrics, err
	}
	var createdAt string
	err = store.db.QueryRowContext(ctx, `
	    SELECT COALESCE(MIN(created_at), '') FROM tasks WHERE state IN (?, ?, ?, ?)
	`, model.TaskWaitingConfirmation, model.TaskQueued, model.TaskRunning, model.TaskRollingBack).Scan(&createdAt)
	if err != nil {
		return metrics, err
	}
	if createdAt != "" {
		created, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return metrics, err
		}
		metrics.OldestActiveAge = store.now().Sub(created).Seconds()
	}
	var snapshot string
	err = store.db.QueryRowContext(ctx,
		`SELECT COALESCE((SELECT value FROM metadata WHERE key = 'last_snapshot_at'), '')`).Scan(&snapshot)
	if err != nil {
		return metrics, err
	}
	if snapshot != "" {
		value, err := time.Parse(time.RFC3339Nano, snapshot)
		if err != nil {
			return metrics, err
		}
		metrics.LastSnapshotEpoch = float64(value.Unix())
	}
	return metrics, nil
}

func (store *Store) Prune(ctx context.Context, detailRetention, summaryRetention time.Duration) error {
	detailBefore := timeText(store.now().Add(-detailRetention))
	summaryBefore := timeText(store.now().Add(-summaryRetention))
	statements := []struct {
		query string
		arg   string
	}{
		{`DELETE FROM events WHERE occurred_at < ?`, detailBefore},
		{`DELETE FROM previews WHERE expires_at < ?`, detailBefore},
		{`DELETE FROM audit_entries WHERE occurred_at < ?`, summaryBefore},
		{`DELETE FROM release_plans WHERE state IN ('completed', 'invalidated') AND updated_at < ?`, summaryBefore},
		{`DELETE FROM recovery_points WHERE task_id IN (
			SELECT id FROM tasks WHERE finished_at IS NOT NULL AND finished_at < ?
		)`, summaryBefore},
		{`DELETE FROM tasks WHERE finished_at IS NOT NULL AND finished_at < ?`, summaryBefore},
	}
	for _, statement := range statements {
		if _, err := store.db.ExecContext(ctx, statement.query, statement.arg); err != nil {
			return err
		}
	}
	return nil
}

func (store *Store) Snapshot(ctx context.Context, directory string) (string, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", err
	}
	now := store.now()
	path := filepath.Join(directory, "ops-"+now.Format("20060102T150405Z")+".db")
	quoted := "'" + strings.ReplaceAll(path, "'", "''") + "'"
	if _, err := store.db.ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		return "", fmt.Errorf("创建 SQLite 快照失败: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	_, err := store.db.ExecContext(ctx, `
        INSERT INTO metadata(key, value) VALUES('last_snapshot_at', ?)
        ON CONFLICT(key) DO UPDATE SET value = excluded.value
    `, timeText(now))
	return path, err
}
