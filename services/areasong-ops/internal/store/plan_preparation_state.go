package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"math"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type PlanPreparationMutation struct {
	ID               string `json:"id"`
	PlanID           string `json:"planId"`
	IdempotencyKey   string `json:"idempotencyKey"`
	ActorHash        string `json:"actorHash"`
	RequestDigest    string `json:"requestDigest"`
	ScopeDigest      string `json:"scopeDigest"`
	OwnerToken       string `json:"-"`
	ExpectedState    string `json:"expectedState"`
	ExpectedRevision int64  `json:"expectedRevision"`
}

func matchPreparationOwner(r model.PlanPreparation, owner string, m PlanPreparationMutation) error {
	if !model.ValidWorkDigest(m.OwnerToken) || subtle.ConstantTimeCompare([]byte(owner), []byte(model.WorkDigest(m.OwnerToken))) != 1 ||
		r.ID != m.ID || r.Request.PlanID != m.PlanID || r.Request.IdempotencyKey != m.IdempotencyKey ||
		r.Request.Authority.ActorHash != m.ActorHash || r.RequestDigest != m.RequestDigest || r.Request.Binding.ScopeDigest != m.ScopeDigest {
		return ErrWorkAdmissionConflict
	}
	return nil
}

func matchPreparationRevision(r model.PlanPreparation, m PlanPreparationMutation) error {
	if r.State != m.ExpectedState || r.Revision != m.ExpectedRevision || r.Revision <= 0 || r.Revision == math.MaxInt64 {
		return ErrWorkAdmissionConflict
	}
	return nil
}

type preparationUpdate struct {
	state, resultJSON, resultDigest, planID, closeKind, closeJSON string
}

func (store *Store) updatePreparationTx(ctx context.Context, tx *sql.Tx, r model.PlanPreparation, m PlanPreparationMutation, u preparationUpdate) (model.PlanPreparation, error) {
	var closed any
	digest := ""
	now := timeText(store.now())
	if u.state == model.WorkClosed {
		closed = now
		digest = model.WorkDigest(u.closeJSON)
	}
	if u.resultJSON == "" {
		u.resultJSON, u.resultDigest = r.ResultJSON, r.ResultDigest
	}
	result, err := tx.ExecContext(ctx, `UPDATE release_plan_preparations SET state=?,revision=revision+1,result_json=?,result_digest=?,
 produced_plan_id=?,closed_at=?,close_kind=?,close_evidence_json=?,close_digest=?,updated_at=?
 WHERE id=? AND plan_id=? AND idempotency_key=? AND actor_hash=? AND request_digest=? AND owner_token_hash=?
 AND state=? AND revision=? AND revision<9223372036854775807`,
		u.state, u.resultJSON, u.resultDigest, workNullable(u.planID), closed, u.closeKind, u.closeJSON, digest, now,
		m.ID, m.PlanID, m.IdempotencyKey, m.ActorHash, m.RequestDigest, model.WorkDigest(m.OwnerToken), m.ExpectedState, m.ExpectedRevision)
	if err = requireOne(result, err, "创建检查比较更新失败"); err != nil {
		return model.PlanPreparation{}, err
	}
	updated, _, err := readPlanPreparationTx(ctx, tx, m.ID)
	return updated, err
}

func (store *Store) BeginPlanInspection(ctx context.Context, m PlanPreparationMutation) (model.PlanPreparation, bool, error) {
	r, err := store.advancePreparation(ctx, m, model.WorkPreparing, nil)
	return r, err == nil, err
}

func (store *Store) MarkPlanPreparationUncertain(ctx context.Context, m PlanPreparationMutation) (model.PlanPreparation, error) {
	return store.advancePreparation(ctx, m, model.WorkUncertain, nil)
}

func (store *Store) RecordPlanInspectionDone(ctx context.Context, m PlanPreparationMutation, result model.PlanInspectionResult) (model.PlanPreparation, error) {
	return store.advancePreparation(ctx, m, model.PlanInspectionDone, &result)
}

func (store *Store) advancePreparation(ctx context.Context, m PlanPreparationMutation, next string, result *model.PlanInspectionResult) (model.PlanPreparation, error) {
	tx, err := store.beginRegisteredWorkTx(ctx)
	if err != nil {
		return model.PlanPreparation{}, err
	}
	defer tx.Rollback()
	r, owner, err := readPlanPreparationTx(ctx, tx.Tx, m.ID)
	if err != nil {
		return r, err
	}
	if err = matchPreparationOwner(r, owner, m); err != nil {
		return r, err
	}
	if err = matchPreparationRevision(r, m); err != nil {
		return r, err
	}
	u := preparationUpdate{state: next}
	switch next {
	case model.WorkPreparing:
		if r.State != model.WorkAdmitted {
			return r, ErrWorkAdmissionConflict
		}
		if err = validatePreparationTargetsTx(ctx, tx.Tx, r.Request); err != nil {
			return r, err
		}
	case model.PlanInspectionDone:
		if r.State != model.WorkPreparing || result == nil || result.ScopeDigest != r.Request.Binding.ScopeDigest {
			return r, ErrWorkAdmissionConflict
		}
		if u.resultJSON, u.resultDigest, err = result.Canonical(); err != nil {
			return r, err
		}
	case model.WorkUncertain:
		if r.State != model.WorkAdmitted && r.State != model.WorkPreparing && r.State != model.PlanInspectionDone {
			return r, ErrWorkAdmissionConflict
		}
	default:
		return r, ErrWorkAdmissionConflict
	}
	r, err = store.updatePreparationTx(ctx, tx.Tx, r, m, u)
	if err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return model.PlanPreparation{}, err
	}
	return r, nil
}

func (store *Store) ClosePlanPreparationNoWork(ctx context.Context, m PlanPreparationMutation) (model.PlanPreparation, error) {
	return store.finishPreparation(ctx, m, nil, "no_work")
}

// nil plan 只关闭已证明安全的检查失败，不创建任务或伪造计划终态。
func (store *Store) FinishPreparedReleasePlan(ctx context.Context, m PlanPreparationMutation, plan *ReleasePlanInput) (model.PlanPreparation, error) {
	return store.finishPreparation(ctx, m, plan, "inspection_settled")
}

func (store *Store) finishPreparation(ctx context.Context, m PlanPreparationMutation, input *ReleasePlanInput, kind string) (model.PlanPreparation, error) {
	tx, err := store.beginRegisteredWorkTx(ctx)
	if err != nil {
		return model.PlanPreparation{}, err
	}
	defer tx.Rollback()
	r, owner, err := readPlanPreparationTx(ctx, tx.Tx, m.ID)
	if err != nil {
		return r, err
	}
	if err = matchPreparationOwner(r, owner, m); err != nil {
		return r, err
	}
	planDigest := ""
	if input != nil {
		planDigest = input.Plan.Digest
	}
	closeRaw, _ := json.Marshal(struct {
		Mutation     PlanPreparationMutation `json:"mutation"`
		Kind         string                  `json:"kind"`
		ResultDigest string                  `json:"resultDigest"`
		PlanDigest   string                  `json:"planDigest"`
	}{m, kind, r.ResultDigest, planDigest})
	if r.State == model.WorkClosed {
		if r.CloseEvidenceJSON != string(closeRaw) {
			return r, ErrWorkAdmissionConflict
		}
		return r, tx.Commit()
	}
	if err = matchPreparationRevision(r, m); err != nil {
		return r, err
	}
	u := preparationUpdate{state: model.WorkClosed, closeKind: kind, closeJSON: string(closeRaw)}
	if kind == "no_work" {
		if r.State != model.WorkAdmitted || input != nil {
			return r, ErrWorkAdmissionConflict
		}
	} else {
		if r.State != model.PlanInspectionDone {
			return r, ErrWorkAdmissionConflict
		}
		if err = store.finishPreparedPlanTx(ctx, tx.Tx, r, input); err != nil {
			return r, err
		}
		if input != nil {
			u.planID = input.Plan.ID
		}
	}
	r, err = store.updatePreparationTx(ctx, tx.Tx, r, m, u)
	if err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return model.PlanPreparation{}, err
	}
	return r, nil
}

func (store *Store) finishPreparedPlanTx(ctx context.Context, tx *sql.Tx, r model.PlanPreparation, input *ReleasePlanInput) error {
	var result model.PlanInspectionResult
	if json.Unmarshal([]byte(r.ResultJSON), &result) != nil {
		return ErrWorkAdmissionCorrupt
	}
	if input == nil {
		if result.Succeeded {
			return ErrWorkAdmissionConflict
		}
		return nil
	}
	if err := matchPreparedPlan(r, input.Plan); err != nil {
		return err
	}
	if err := validatePreparationTargetsTx(ctx, tx, r.Request); err != nil {
		return err
	}
	if input.Plan.State != model.PlanPendingApproval || input.Plan.ApprovedByHash != "" || input.Plan.TaskID != "" ||
		input.Plan.ApprovedAt != nil || input.Plan.SecondApprovedByHash != "" ||
		!input.Plan.HasRequiredApprovalPolicy() || input.ConfirmationHash != HashConfirmation(input.Plan.ConfirmationPhrase) {
		return model.ErrReleaseLifecycle
	}
	if err := store.createReleasePlan(ctx, tx, *input); err != nil {
		return err
	}
	return appendPlanAudit(ctx, tx, releasePlanCreatedAudit(input.Plan), store.now())
}
