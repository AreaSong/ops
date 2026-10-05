package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"math"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type WorkAdmissionMutation struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	WorkID           string `json:"workId"`
	IdempotencyKey   string `json:"idempotencyKey"`
	ActorHash        string `json:"actorHash"`
	ApprovalDigest   string `json:"approvalDigest"`
	RequestDigest    string `json:"requestDigest"`
	OwnerToken       string `json:"-"`
	ExpectedState    string `json:"expectedState"`
	ExpectedRevision int64  `json:"expectedRevision"`
	TaskID           string `json:"taskId"`
}

type WorkAdmissionClose struct {
	Mutation  WorkAdmissionMutation   `json:"mutation"`
	CloseKind string                  `json:"closeKind"`
	Evidence  model.WorkCloseEvidence `json:"evidence"`
}

type workAdmissionUpdate struct {
	State, TaskID, CloseKind, CloseJSON string
}

func matchWorkOwner(record model.WorkAdmission, owner string, input WorkAdmissionMutation) error {
	request := record.Request
	if !model.ValidWorkDigest(input.OwnerToken) || subtle.ConstantTimeCompare([]byte(owner), []byte(model.WorkDigest(input.OwnerToken))) != 1 ||
		record.ID != input.ID || request.Kind != input.Kind || request.WorkID != input.WorkID || request.ActorHash != input.ActorHash ||
		request.IdempotencyKey != input.IdempotencyKey || request.ApprovalDigest != input.ApprovalDigest || record.RequestDigest != input.RequestDigest || record.TaskID != input.TaskID {
		return ErrWorkAdmissionConflict
	}
	return nil
}

func matchWorkRevision(record model.WorkAdmission, input WorkAdmissionMutation) error {
	if input.ExpectedRevision <= 0 || record.Revision != input.ExpectedRevision || record.Revision == math.MaxInt64 || record.State != input.ExpectedState {
		return ErrWorkAdmissionConflict
	}
	return nil
}

func workNullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (store *Store) updateRegisteredWorkTx(ctx context.Context, tx *sql.Tx, input WorkAdmissionMutation, next workAdmissionUpdate) (model.WorkAdmission, error) {
	now := timeText(store.now())
	var closed any
	closeDigest := ""
	if next.State == model.WorkClosed {
		closed = now
		closeDigest = model.WorkDigest(next.CloseJSON)
	}
	result, err := tx.ExecContext(ctx, `UPDATE work_admissions SET state=?,revision=revision+1,task_id=?,updated_at=?,closed_at=?,close_kind=?,close_digest=?,close_evidence_json=?
 WHERE id=? AND kind=? AND work_id=? AND idempotency_key=? AND actor_hash=? AND approval_digest=? AND request_digest=? AND owner_token_hash=? AND state=? AND revision=? AND revision<9223372036854775807 AND task_id IS ?`,
		next.State, workNullable(next.TaskID), now, closed, next.CloseKind, closeDigest, next.CloseJSON, input.ID, input.Kind, input.WorkID, input.IdempotencyKey, input.ActorHash, input.ApprovalDigest, input.RequestDigest, model.WorkDigest(input.OwnerToken), input.ExpectedState, input.ExpectedRevision, workNullable(input.TaskID))
	if err = requireOne(result, err, "工作准入比较更新失败"); err != nil {
		return model.WorkAdmission{}, err
	}
	record, _, _, err := readWorkAdmissionTx(ctx, tx, input.ID)
	return record, err
}

// Advanced只有本次提交成功才为true；响应不明后的重放不能重新获得副作用执行权。
func (store *Store) BeginWorkPreparation(ctx context.Context, input WorkAdmissionMutation) (model.WorkAdmission, bool, error) {
	record, err := store.advanceRegisteredWork(ctx, input, model.WorkPreparing)
	return record, err == nil, err
}

func (store *Store) MarkWorkUncertain(ctx context.Context, input WorkAdmissionMutation) (model.WorkAdmission, error) {
	return store.advanceRegisteredWork(ctx, input, model.WorkUncertain)
}

func (store *Store) advanceRegisteredWork(ctx context.Context, input WorkAdmissionMutation, next string) (model.WorkAdmission, error) {
	tx, err := store.beginRegisteredWorkTx(ctx)
	if err != nil {
		return model.WorkAdmission{}, err
	}
	defer tx.Rollback()
	record, owner, _, err := readWorkAdmissionTx(ctx, tx.Tx, input.ID)
	if err != nil {
		return model.WorkAdmission{}, err
	}
	if err = matchWorkOwner(record, owner, input); err != nil {
		return model.WorkAdmission{}, err
	}
	if err = matchWorkRevision(record, input); err != nil {
		return model.WorkAdmission{}, err
	}
	if next == model.WorkPreparing {
		if record.State != model.WorkAdmitted {
			return model.WorkAdmission{}, ErrWorkAdmissionConflict
		}
		if err = validateWorkTargetsTx(ctx, tx.Tx, record.Request.Targets); err != nil {
			return model.WorkAdmission{}, err
		}
	} else if record.State != model.WorkAdmitted && record.State != model.WorkPreparing && record.State != model.WorkTaskBound {
		return model.WorkAdmission{}, ErrWorkAdmissionConflict
	}
	record, err = store.updateRegisteredWorkTx(ctx, tx.Tx, input, workAdmissionUpdate{State: next, TaskID: record.TaskID})
	if err != nil {
		return model.WorkAdmission{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.WorkAdmission{}, err
	}
	return record, nil
}

// 仅供后续同包入队事务接线；B只由合成测试调用，不单独提交或创建任务。
func (store *Store) bindRegisteredWorkTaskTx(ctx context.Context, tx *sql.Tx, input WorkAdmissionMutation, taskID string) (model.WorkAdmission, error) {
	record, owner, _, err := readWorkAdmissionTx(ctx, tx, input.ID)
	if err != nil {
		return model.WorkAdmission{}, err
	}
	if err = matchWorkOwner(record, owner, input); err != nil {
		return model.WorkAdmission{}, err
	}
	if err = matchWorkRevision(record, input); err != nil {
		return model.WorkAdmission{}, err
	}
	if record.State != model.WorkPreparing || record.TaskID != "" || taskID == "" {
		return model.WorkAdmission{}, ErrWorkAdmissionConflict
	}
	task, err := registeredTaskTx(ctx, tx, record, taskID)
	if err != nil {
		return model.WorkAdmission{}, err
	}
	if task.State != model.TaskQueued {
		return model.WorkAdmission{}, ErrWorkAdmissionConflict
	}
	if err = validateWorkTargetsTx(ctx, tx, record.Request.Targets); err != nil {
		return model.WorkAdmission{}, err
	}
	return store.updateRegisteredWorkTx(ctx, tx, input, workAdmissionUpdate{State: model.WorkTaskBound, TaskID: taskID})
}

func registeredTaskTx(ctx context.Context, tx *sql.Tx, record model.WorkAdmission, taskID string) (model.Task, error) {
	task, err := scanTask(tx.QueryRowContext(ctx, taskSelect+` WHERE id=?`, taskID))
	if err != nil {
		return task, err
	}
	r := record.Request
	if task.PlanID != r.WorkID || task.PlanDigest != r.ApprovalDigest || task.ActorHash != r.ActorHash || task.IdempotencyKey != r.IdempotencyKey || task.RequestHash != HashConfirmation(r.WorkID+"\x00"+r.ApprovalDigest) {
		return task, ErrWorkAdmissionConflict
	}
	return task, nil
}

func (store *Store) CloseRegisteredWork(ctx context.Context, input WorkAdmissionClose) (model.WorkAdmission, error) {
	tx, err := store.beginRegisteredWorkTx(ctx)
	if err != nil {
		return model.WorkAdmission{}, err
	}
	defer tx.Rollback()
	record, err := store.closeRegisteredWorkTx(ctx, tx.Tx, input)
	if err != nil {
		return model.WorkAdmission{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.WorkAdmission{}, err
	}
	return record, nil
}

func (store *Store) closeRegisteredWorkTx(ctx context.Context, tx *sql.Tx, input WorkAdmissionClose) (model.WorkAdmission, error) {
	if err := input.Evidence.Validate(input.CloseKind); err != nil {
		return model.WorkAdmission{}, err
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return model.WorkAdmission{}, err
	}
	record, owner, _, err := readWorkAdmissionTx(ctx, tx, input.Mutation.ID)
	if err != nil {
		return model.WorkAdmission{}, err
	}
	if err = matchWorkOwner(record, owner, input.Mutation); err != nil {
		return model.WorkAdmission{}, err
	}
	if record.State == model.WorkClosed {
		if record.CloseEvidenceJSON != string(raw) || record.CloseDigest != model.WorkDigest(string(raw)) {
			return model.WorkAdmission{}, ErrWorkAdmissionConflict
		}
		return record, nil
	}
	if err = matchWorkRevision(record, input.Mutation); err != nil {
		return model.WorkAdmission{}, err
	}
	if err = validateWorkClosureTx(ctx, tx, record, input.CloseKind); err != nil {
		return model.WorkAdmission{}, err
	}
	record, err = store.updateRegisteredWorkTx(ctx, tx, input.Mutation, workAdmissionUpdate{State: model.WorkClosed, TaskID: record.TaskID, CloseKind: input.CloseKind, CloseJSON: string(raw)})
	if err != nil {
		return model.WorkAdmission{}, err
	}
	return record, nil
}

func validateWorkClosureTx(ctx context.Context, tx *sql.Tx, record model.WorkAdmission, kind string) error {
	if kind == "no_work" {
		if record.State == model.WorkAdmitted && record.TaskID == "" {
			return nil
		}
		return ErrWorkAdmissionConflict
	}
	if record.State != model.WorkTaskBound || record.TaskID == "" {
		return ErrWorkAdmissionConflict
	}
	task, err := registeredTaskTx(ctx, tx, record, record.TaskID)
	if err != nil {
		return err
	}
	if task.FinishedAt == nil || task.FinishedAt.IsZero() {
		return ErrWorkAdmissionConflict
	}
	switch task.State {
	case model.TaskSucceeded, model.TaskFailed, model.TaskFailedRecoverable, model.TaskRolledBack:
		return nil
	default:
		return ErrWorkAdmissionConflict
	}
}
