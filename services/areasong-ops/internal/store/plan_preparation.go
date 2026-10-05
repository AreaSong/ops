package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type PlanPreparationInput struct {
	ID         string
	OwnerToken string `json:"-"`
	Request    model.PlanPreparationRequest
}

const preparationSelect = `SELECT id,request_json,request_digest,authority_digest,owner_token_hash,state,revision,
 result_json,result_digest,produced_plan_id,created_at,updated_at,closed_at,close_kind,close_evidence_json,close_digest,
 kind,plan_id,idempotency_key,actor_hash FROM release_plan_preparations`

func (store *Store) AdmitPlanPreparation(ctx context.Context, input PlanPreparationInput) (model.PlanPreparation, bool, error) {
	raw, digest, authority, err := model.CanonicalPlanPreparation(input.Request)
	if err != nil {
		return model.PlanPreparation{}, false, err
	}
	if input.ID != input.Request.Binding.PreparationID || !model.ValidWorkDigest(input.OwnerToken) {
		return model.PlanPreparation{}, false, model.ErrReleaseLifecycle
	}
	tx, err := store.beginRegisteredWorkTx(ctx)
	if err != nil {
		return model.PlanPreparation{}, false, err
	}
	defer tx.Rollback()
	var id string
	r := input.Request
	err = tx.QueryRowContext(ctx, `SELECT id FROM release_plan_preparations WHERE id=? OR plan_id=? OR idempotency_key=? LIMIT 1`, input.ID, r.PlanID, r.IdempotencyKey).Scan(&id)
	if err == nil {
		record, _, e := readPlanPreparationTx(ctx, tx.Tx, id)
		if e != nil {
			return record, false, e
		}
		existing, _, _, e := model.CanonicalPlanPreparation(record.Request)
		if e != nil || existing != raw {
			return record, false, ErrIdempotency
		}
		return record, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.PlanPreparation{}, false, err
	}
	if err = validatePreparationTargetsTx(ctx, tx.Tx, r); err != nil {
		return model.PlanPreparation{}, false, err
	}
	now := timeText(store.now())
	_, err = tx.ExecContext(ctx, `INSERT INTO release_plan_preparations(id,kind,plan_id,idempotency_key,actor_hash,authority_digest,
 request_digest,request_json,owner_token_hash,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		input.ID, r.Kind, r.PlanID, r.IdempotencyKey, r.Authority.ActorHash, authority, digest, raw, model.WorkDigest(input.OwnerToken), now, now)
	if err != nil {
		return model.PlanPreparation{}, false, err
	}
	targets, _ := r.Binding.WorkTargets()
	for _, t := range targets {
		if _, err = tx.ExecContext(ctx, `INSERT INTO release_plan_preparation_targets(preparation_id,tenant_id,expected_generation) VALUES(?,?,?)`, input.ID, t.TenantID, t.ExpectedGeneration); err != nil {
			return model.PlanPreparation{}, false, err
		}
	}
	record, _, err := readPlanPreparationTx(ctx, tx.Tx, input.ID)
	if err != nil {
		return record, false, err
	}
	if err = tx.Commit(); err != nil {
		return model.PlanPreparation{}, false, err
	}
	return record, true, nil
}

func (store *Store) GetPlanPreparation(ctx context.Context, id string) (model.PlanPreparation, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.PlanPreparation{}, err
	}
	defer tx.Rollback()
	record, _, err := readPlanPreparationTx(ctx, tx, id)
	if err != nil {
		return record, err
	}
	return record, tx.Commit()
}

func (store *Store) GetPlanPreparationByRequest(ctx context.Context, key string) (model.PlanPreparation, bool, error) {
	var id string
	err := store.db.QueryRowContext(ctx, `SELECT id FROM release_plan_preparations WHERE idempotency_key=?`, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.PlanPreparation{}, false, nil
	}
	if err != nil {
		return model.PlanPreparation{}, false, err
	}
	record, err := store.GetPlanPreparation(ctx, id)
	return record, true, err
}

func readPlanPreparationTx(ctx context.Context, tx *sql.Tx, id string) (model.PlanPreparation, string, error) {
	var r model.PlanPreparation
	var raw, owner, created, updated, kind, planID, key, actor string
	var plan, closed sql.NullString
	err := tx.QueryRowContext(ctx, preparationSelect+" WHERE id=?", id).Scan(&r.ID, &raw, &r.RequestDigest, &r.AuthorityDigest, &owner,
		&r.State, &r.Revision, &r.ResultJSON, &r.ResultDigest, &plan, &created, &updated, &closed, &r.CloseKind, &r.CloseEvidenceJSON, &r.CloseDigest,
		&kind, &planID, &key, &actor)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if err != nil {
		return r, "", err
	}
	if json.Unmarshal([]byte(raw), &r.Request) != nil {
		return r, "", ErrWorkAdmissionCorrupt
	}
	canonical, digest, authority, err := model.CanonicalPlanPreparation(r.Request)
	if err != nil || canonical != raw || digest != r.RequestDigest || authority != r.AuthorityDigest ||
		r.Request.Binding.PreparationID != r.ID || kind != r.Request.Kind || planID != r.Request.PlanID || key != r.Request.IdempotencyKey ||
		actor != r.Request.Authority.ActorHash || !model.ValidWorkDigest(owner) {
		return r, "", ErrWorkAdmissionCorrupt
	}
	r.ProducedPlanID = plan.String
	if r.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return r, "", err
	}
	if r.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return r, "", err
	}
	if closed.Valid {
		v, e := time.Parse(time.RFC3339Nano, closed.String)
		if e != nil {
			return r, "", e
		}
		r.ClosedAt = &v
	}
	if err = verifyPreparationRows(ctx, tx, r); err != nil {
		return r, "", err
	}
	return r, owner, nil
}

func verifyPreparationRows(ctx context.Context, tx *sql.Tx, r model.PlanPreparation) error {
	targets, _ := r.Request.Binding.WorkTargets()
	rows, err := tx.QueryContext(ctx, `SELECT tenant_id,expected_generation FROM release_plan_preparation_targets WHERE preparation_id=? ORDER BY tenant_id`, r.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	actual := []model.WorkAdmissionTarget{}
	for rows.Next() {
		var t model.WorkAdmissionTarget
		if err = rows.Scan(&t.TenantID, &t.ExpectedGeneration); err != nil {
			return err
		}
		actual = append(actual, t)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if !sameReleaseJSON(actual, targets) {
		return ErrWorkAdmissionCorrupt
	}
	if r.ResultJSON != "" {
		var result model.PlanInspectionResult
		if json.Unmarshal([]byte(r.ResultJSON), &result) != nil {
			return ErrWorkAdmissionCorrupt
		}
		raw, digest, e := result.Canonical()
		if e != nil || raw != r.ResultJSON || digest != r.ResultDigest || result.ScopeDigest != r.Request.Binding.ScopeDigest {
			return ErrWorkAdmissionCorrupt
		}
	}
	if r.State == model.WorkClosed && (r.CloseDigest != model.WorkDigest(r.CloseEvidenceJSON) || strings.TrimSpace(r.CloseEvidenceJSON) == "") {
		return ErrWorkAdmissionCorrupt
	}
	return nil
}

func assertNoUnfinishedPlanPreparationTx(ctx context.Context, tx *sql.Tx, tenantID string) error {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT p.id FROM release_plan_preparation_targets t JOIN release_plan_preparations p ON p.id=t.preparation_id WHERE t.tenant_id=? AND p.state<>'closed' LIMIT 1`, tenantID).Scan(&id)
	if err == nil {
		return ErrUnfinishedRegisteredWork
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}
