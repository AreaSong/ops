package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const fleetRunnerUpdateReceiptSelect = `SELECT item_id,assignment_generation,plan_id,claim_token,
	control_plane_endpoint,assignment_json,local_update_id,action,state,last_error,created_at,updated_at
	FROM runner_fleet_update_receipts`

func (store *Store) SaveFleetRunnerUpdateReceipt(
	ctx context.Context,
	receipt model.FleetRunnerUpdateReceipt,
) (model.FleetRunnerUpdateReceipt, bool, error) {
	if receipt.ItemID == "" || receipt.AssignmentGeneration == 0 || receipt.PlanID == "" ||
		receipt.Fence.ClaimToken == "" || receipt.ControlPlaneEndpoint == "" ||
		(receipt.Action != "update" && receipt.Action != "rollback") ||
		receipt.Assignment.ItemID != receipt.ItemID || receipt.Assignment.PlanID != receipt.PlanID ||
		receipt.Assignment.Fence != receipt.Fence {
		return model.FleetRunnerUpdateReceipt{}, false, errors.New("Runner Fleet 更新本地回执不完整")
	}
	assignmentJSON, err := json.Marshal(receipt.Assignment)
	if err != nil {
		return model.FleetRunnerUpdateReceipt{}, false, err
	}
	existing, found, err := store.GetFleetRunnerUpdateReceipt(ctx, receipt.ItemID, receipt.AssignmentGeneration)
	if err != nil {
		return model.FleetRunnerUpdateReceipt{}, false, err
	}
	if found {
		if !sameFleetRunnerUpdateReceiptIdentity(existing, receipt) {
			return model.FleetRunnerUpdateReceipt{}, false, ErrIdempotency
		}
		return existing, false, nil
	}
	now := store.now()
	receipt.State, receipt.CreatedAt, receipt.UpdatedAt = "prepared", now, now
	_, err = store.db.ExecContext(ctx, `INSERT INTO runner_fleet_update_receipts(
		item_id,assignment_generation,plan_id,claim_token,control_plane_endpoint,assignment_json,
		local_update_id,action,state,last_error,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		receipt.ItemID, receipt.AssignmentGeneration, receipt.PlanID, receipt.Fence.ClaimToken,
		receipt.ControlPlaneEndpoint, string(assignmentJSON), receipt.LocalUpdateID, receipt.Action, receipt.State,
		receipt.LastError, timeText(now), timeText(now))
	if err != nil {
		return model.FleetRunnerUpdateReceipt{}, false, err
	}
	return receipt, true, nil
}

func sameFleetRunnerUpdateReceiptIdentity(left, right model.FleetRunnerUpdateReceipt) bool {
	return left.ItemID == right.ItemID && left.AssignmentGeneration == right.AssignmentGeneration &&
		left.PlanID == right.PlanID && left.Fence.ClaimToken == right.Fence.ClaimToken &&
		left.ControlPlaneEndpoint == right.ControlPlaneEndpoint && left.LocalUpdateID == right.LocalUpdateID &&
		left.Action == right.Action && left.Assignment == right.Assignment
}

func (store *Store) GetFleetRunnerUpdateReceipt(
	ctx context.Context,
	itemID string,
	generation uint64,
) (model.FleetRunnerUpdateReceipt, bool, error) {
	receipt, err := scanFleetRunnerUpdateReceipt(store.db.QueryRowContext(ctx,
		fleetRunnerUpdateReceiptSelect+` WHERE item_id=? AND assignment_generation=?`, itemID, generation))
	if errors.Is(err, sql.ErrNoRows) {
		return model.FleetRunnerUpdateReceipt{}, false, nil
	}
	return receipt, err == nil, err
}

func (store *Store) ListPendingFleetRunnerUpdateReceipts(
	ctx context.Context,
) ([]model.FleetRunnerUpdateReceipt, error) {
	rows, err := store.db.QueryContext(ctx, fleetRunnerUpdateReceiptSelect+
		` WHERE state IN ('prepared','launching','launched','needs_attention') ORDER BY created_at,item_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	receipts := make([]model.FleetRunnerUpdateReceipt, 0)
	for rows.Next() {
		receipt, scanErr := scanFleetRunnerUpdateReceipt(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		receipts = append(receipts, receipt)
	}
	return receipts, rows.Err()
}

func (store *Store) GetReportedFleetRunnerUpdateReceipt(
	ctx context.Context,
	planID, itemID string,
) (model.FleetRunnerUpdateReceipt, bool, error) {
	receipt, err := scanFleetRunnerUpdateReceipt(store.db.QueryRowContext(ctx,
		fleetRunnerUpdateReceiptSelect+` WHERE plan_id=? AND item_id=? AND action='update'
			AND state='reported' ORDER BY assignment_generation DESC LIMIT 1`, planID, itemID))
	if errors.Is(err, sql.ErrNoRows) {
		return model.FleetRunnerUpdateReceipt{}, false, nil
	}
	return receipt, err == nil, err
}

func (store *Store) UpdateFleetRunnerUpdateReceipt(
	ctx context.Context,
	itemID string,
	generation uint64,
	state, localUpdateID, lastError string,
) error {
	if state != "launching" && state != "launched" && state != "reported" && state != "needs_attention" {
		return errors.New("Runner Fleet 更新本地回执状态无效")
	}
	now := store.now()
	result, err := store.db.ExecContext(ctx, `UPDATE runner_fleet_update_receipts SET state=?,
		local_update_id=CASE WHEN ?='' THEN local_update_id ELSE ? END,last_error=?,updated_at=?
		WHERE item_id=? AND assignment_generation=? AND (
			(state='prepared' AND ? IN ('launching','needs_attention','reported')) OR
			(state='launching' AND ? IN ('launched','needs_attention','reported')) OR
			(state='launched' AND ? IN ('needs_attention','reported')) OR
			(state='needs_attention' AND ?='reported'))`, state,
		localUpdateID, localUpdateID, lastError, timeText(now), itemID, generation,
		state, state, state, state)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	existing, found, err := store.GetFleetRunnerUpdateReceipt(ctx, itemID, generation)
	if err != nil {
		return err
	}
	if found && existing.State == state && (localUpdateID == "" || existing.LocalUpdateID == localUpdateID) {
		return nil
	}
	return errors.New("Runner Fleet 更新本地回执状态已变化")
}

func scanFleetRunnerUpdateReceipt(scanner interface{ Scan(...any) error }) (model.FleetRunnerUpdateReceipt, error) {
	var receipt model.FleetRunnerUpdateReceipt
	var token, assignmentJSON, created, updated string
	err := scanner.Scan(&receipt.ItemID, &receipt.AssignmentGeneration, &receipt.PlanID, &token,
		&receipt.ControlPlaneEndpoint, &assignmentJSON, &receipt.LocalUpdateID, &receipt.Action, &receipt.State,
		&receipt.LastError, &created, &updated)
	if err != nil {
		return model.FleetRunnerUpdateReceipt{}, err
	}
	receipt.Fence = model.FleetRunnerUpdateFence{
		Generation: receipt.AssignmentGeneration,
		ClaimToken: token,
	}
	if err = json.Unmarshal([]byte(assignmentJSON), &receipt.Assignment); err != nil {
		return model.FleetRunnerUpdateReceipt{}, err
	}
	if receipt.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err == nil {
		receipt.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	}
	return receipt, err
}
