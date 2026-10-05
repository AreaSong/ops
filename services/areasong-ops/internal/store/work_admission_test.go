package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"math"
	"reflect"
	"strings"
	"testing"
)

func workInput(id string) WorkAdmissionInput {
	return WorkAdmissionInput{AdmissionID: "admission-" + id, OwnerToken: strings.Repeat("c", 64), Request: model.WorkAdmissionRequest{Version: 1, Kind: model.WorkAdmissionKind, WorkID: "plan-" + id, IdempotencyKey: "request-" + id, ActorHash: lifecycleActor, ApprovalDigest: strings.Repeat("d", 64), Targets: []model.WorkAdmissionTarget{{TenantID: "team", ExpectedGeneration: 1}, {TenantID: "other", ExpectedGeneration: 1}}}}
}
func workMutation(record model.WorkAdmission) WorkAdmissionMutation {
	r := record.Request
	return WorkAdmissionMutation{ID: record.ID, Kind: r.Kind, WorkID: r.WorkID, IdempotencyKey: r.IdempotencyKey, ActorHash: r.ActorHash, ApprovalDigest: r.ApprovalDigest, RequestDigest: record.RequestDigest, OwnerToken: strings.Repeat("c", 64), ExpectedState: record.State, ExpectedRevision: record.Revision, TaskID: record.TaskID}
}
func admitFixture(t *testing.T, db *Store, id string) model.WorkAdmission {
	t.Helper()
	record, created, err := db.AdmitWork(context.Background(), workInput(id))
	if err != nil || !created {
		t.Fatalf("admit: %v %v", created, err)
	}
	return record
}
func prepareFixture(t *testing.T, db *Store, id string) model.WorkAdmission {
	t.Helper()
	record := admitFixture(t, db, id)
	record, advanced, err := db.BeginWorkPreparation(context.Background(), workMutation(record))
	if err != nil || !advanced {
		t.Fatalf("prepare: %v", err)
	}
	return record
}
func insertWorkTask(ctx context.Context, tx *sql.Tx, r model.WorkAdmission, id string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO tasks(id,idempotency_key,request_hash,actor_hash,service,action,target,risk,state,preview_id,plan_id,plan_digest,snapshot_json,stages_json,created_at) VALUES(?,?,?,?,'synthetic','inspect','','low','queued','',?,?,'{}','[]','2026-01-01T00:00:00Z')`, id, r.Request.IdempotencyKey, HashConfirmation(r.Request.WorkID+"\x00"+r.Request.ApprovalDigest), r.Request.ActorHash, r.Request.WorkID, r.Request.ApprovalDigest)
	return err
}
func bindFixture(t *testing.T, db *Store, r model.WorkAdmission) model.WorkAdmission {
	t.Helper()
	ctx := context.Background()
	tx, err := db.beginRegisteredWorkTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	id := "task-" + r.ID
	if err = insertWorkTask(ctx, tx.Tx, r, id); err != nil {
		t.Fatal(err)
	}
	record, err := db.bindRegisteredWorkTaskTx(ctx, tx.Tx, workMutation(r), id)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return record
}
func closeInput(r model.WorkAdmission) WorkAdmissionClose {
	result := WorkAdmissionClose{Mutation: workMutation(r), CloseKind: "no_work", Evidence: model.WorkCloseEvidence{Kind: "never_started_v1"}}
	if r.TaskID != "" {
		result.CloseKind = "settled"
		result.Evidence = model.WorkCloseEvidence{Kind: "execution_and_cleanup_settled_v1", ExecutorStopped: true, CleanupConfirmed: true, ResultDigest: strings.Repeat("e", 64), CleanupDigest: strings.Repeat("f", 64)}
	}
	return result
}
func TestWorkAdmissionTargetsAndReplay(t *testing.T) {
	db, _ := lifecycleFixture(t)
	ctx := context.Background()
	r := admitFixture(t, db, "one")
	before := lifecycleContents(t, db)
	input := workInput("one")
	input.AdmissionID = "different-attempt"
	input.OwnerToken = strings.Repeat("b", 64)
	got, created, err := db.AdmitWork(ctx, input)
	if err != nil || created || got.ID != r.ID {
		t.Fatalf("replay: %+v %v", got, err)
	}
	assertLifecycleUnchanged(t, db, before)
	if _, _, err = db.BeginWorkPreparation(ctx, WorkAdmissionMutation{ID: r.ID}); err == nil {
		t.Fatal("missing owner accepted")
	}
	for name, change := range map[string]func(*WorkAdmissionInput){
		"key": func(i *WorkAdmissionInput) { i.Request.IdempotencyKey = "other-key" }, "actor": func(i *WorkAdmissionInput) { i.Request.ActorHash = lifecycleApprover },
		"digest": func(i *WorkAdmissionInput) { i.Request.ApprovalDigest = lifecycleApprover }, "generation": func(i *WorkAdmissionInput) { i.Request.Targets[0].ExpectedGeneration = 3 },
		"targets": func(i *WorkAdmissionInput) { i.Request.Targets = i.Request.Targets[:1] },
	} {
		t.Run(name, func(t *testing.T) {
			i := workInput("one")
			change(&i)
			if _, _, e := db.AdmitWork(ctx, i); e == nil {
				t.Fatal("conflict accepted")
			}
			assertLifecycleUnchanged(t, db, before)
		})
	}
}
func TestWorkAdmissionRejectTargetsAtomically(t *testing.T) {
	for _, failure := range []string{"missing", "unknown", "wrong", "disabled", "source", "mirror", "alias", "empty", "wildcard"} {
		t.Run(failure, func(t *testing.T) {
			db, _ := lifecycleFixture(t)
			input := workInput("bad")
			switch failure {
			case "missing":
				input.Request.Targets = append(input.Request.Targets, model.WorkAdmissionTarget{TenantID: "missing", ExpectedGeneration: 1})
			case "unknown":
				lifecycleSQL(t, db, `UPDATE tenants SET lifecycle_generation=0 WHERE id='other'`)
			case "wrong":
				input.Request.Targets[1].ExpectedGeneration = 2
			case "disabled":
				execution := approveLifecycle(t, db, lifecycleRequest(1, 1, "disabled"))
				if _, err := db.applyTenantLifecycleChange(context.Background(), execution); err != nil {
					t.Fatal(err)
				}
			case "source":
				lifecycleSQL(t, db, `UPDATE tenants SET created_by='' WHERE id='other'`)
			case "mirror":
				lifecycleSQL(t, db, `UPDATE tenants SET display_name='drift' WHERE id='other'`)
			case "alias":
				lifecycleSQL(t, db, `INSERT INTO tenants(id,display_name,status,created_at,updated_at,created_by) VALUES('OTHER','alias','active','','','')`)
			case "empty":
				input.Request.Targets = nil
			case "wildcard":
				input.Request.Targets[1].TenantID = "*"
			}
			before := lifecycleContents(t, db)
			if _, created, err := db.AdmitWork(context.Background(), input); err == nil || created {
				t.Fatal("accepted")
			}
			assertLifecycleUnchanged(t, db, before)
		})
	}
}
func TestWorkAdmissionDefaultAndDedup(t *testing.T) {
	db, _ := lifecycleFixture(t)
	input := workInput("default")
	input.Request.Targets = []model.WorkAdmissionTarget{{TenantID: "default", ExpectedGeneration: 1}, {TenantID: "default", ExpectedGeneration: 1}}
	r, _, err := db.AdmitWork(context.Background(), input)
	if err != nil || len(r.Request.Targets) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestWorkAdmissionClosureAndLifecycle(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(fmt.Sprint(bound), func(t *testing.T) {
			db, _ := lifecycleFixture(t)
			ctx := context.Background()
			r := admitFixture(t, db, "close")
			execution := approveLifecycle(t, db, lifecycleRequest(1, 1, "disabled"))
			if bound {
				var err error
				r, _, err = db.BeginWorkPreparation(ctx, workMutation(r))
				if err != nil {
					t.Fatal(err)
				}
				r = bindFixture(t, db, r)
				if _, err = db.CloseRegisteredWork(ctx, closeInput(r)); err == nil {
					t.Fatal("queued closed")
				}
				lifecycleSQL(t, db, `UPDATE tasks SET state='succeeded',finished_at='2026-01-01T00:00:01Z' WHERE id=?`, r.TaskID)
			}
			before := lifecycleContents(t, db)
			if _, err := db.applyTenantLifecycleChange(ctx, execution); err != ErrUnfinishedRegisteredWork {
				t.Fatalf("missing registered block: %v", err)
			}
			assertLifecycleUnchanged(t, db, before)
			input := closeInput(r)
			closed, err := db.CloseRegisteredWork(ctx, input)
			if err != nil || closed.State != model.WorkClosed {
				t.Fatalf("close: %v", err)
			}
			before = lifecycleContents(t, db)
			if _, err = db.CloseRegisteredWork(ctx, input); err != nil {
				t.Fatal(err)
			}
			assertLifecycleUnchanged(t, db, before)
			if _, err = db.applyTenantLifecycleChange(ctx, execution); err != nil {
				t.Fatal(err)
			}
			enable := approveLifecycle(t, db, lifecycleRequest(2, 2, "active"))
			if _, err = db.applyTenantLifecycleChange(ctx, enable); err != nil {
				t.Fatal(err)
			}
			before = lifecycleContents(t, db)
			got, created, err := db.AdmitWork(ctx, workInput("close"))
			if err != nil || created || got.State != model.WorkClosed {
				t.Fatalf("closed replay %v", err)
			}
			if _, advanced, err := db.BeginWorkPreparation(ctx, workMutation(r)); err == nil || advanced {
				t.Fatal("resurrected")
			}
			assertLifecycleUnchanged(t, db, before)
		})
	}
}
func TestWorkAdmissionOwnerAndCloseGuards(t *testing.T) {
	db, _ := lifecycleFixture(t)
	ctx := context.Background()
	r := bindFixture(t, db, prepareFixture(t, db, "guards"))
	lifecycleSQL(t, db, `UPDATE tasks SET state='succeeded',finished_at='2026-01-01T00:00:01Z' WHERE id=?`, r.TaskID)
	changes := map[string]func(*WorkAdmissionClose){
		"owner": func(c *WorkAdmissionClose) { c.Mutation.OwnerToken = lifecycleActor }, "actor": func(c *WorkAdmissionClose) { c.Mutation.ActorHash = lifecycleApprover },
		"work": func(c *WorkAdmissionClose) { c.Mutation.WorkID = "other" }, "kind": func(c *WorkAdmissionClose) { c.Mutation.Kind = "other" }, "key": func(c *WorkAdmissionClose) { c.Mutation.IdempotencyKey = "other" },
		"approval": func(c *WorkAdmissionClose) { c.Mutation.ApprovalDigest = lifecycleActor }, "request": func(c *WorkAdmissionClose) { c.Mutation.RequestDigest = lifecycleActor },
		"task": func(c *WorkAdmissionClose) { c.Mutation.TaskID = "other" }, "state": func(c *WorkAdmissionClose) { c.Mutation.ExpectedState = model.WorkAdmitted }, "revision": func(c *WorkAdmissionClose) { c.Mutation.ExpectedRevision++ },
		"cleanup": func(c *WorkAdmissionClose) { c.Evidence.CleanupConfirmed = false }, "stopped": func(c *WorkAdmissionClose) { c.Evidence.ExecutorStopped = false }, "evidence": func(c *WorkAdmissionClose) { c.Evidence.ResultDigest = "" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			input := closeInput(r)
			change(&input)
			before := lifecycleContents(t, db)
			if _, err := db.CloseRegisteredWork(ctx, input); err == nil {
				t.Fatal("accepted")
			}
			assertLifecycleUnchanged(t, db, before)
		})
	}
	for _, state := range []string{"queued", "running", "needs_attention", "recovery_uncertain", "paused", "unknown"} {
		lifecycleSQL(t, db, `UPDATE tasks SET state=? WHERE id=?`, state, r.TaskID)
		before := lifecycleContents(t, db)
		if _, err := db.CloseRegisteredWork(ctx, closeInput(r)); err == nil {
			t.Fatalf("closed %s", state)
		}
		assertLifecycleUnchanged(t, db, before)
	}
	lifecycleSQL(t, db, `UPDATE tasks SET state='succeeded',plan_digest='wrong' WHERE id=?`, r.TaskID)
	if _, err := db.CloseRegisteredWork(ctx, closeInput(r)); err == nil {
		t.Fatal("changed task accepted")
	}
}
func TestWorkAdmissionUncertainAndCorruption(t *testing.T) {
	db, _ := lifecycleFixture(t)
	ctx := context.Background()
	r := prepareFixture(t, db, "uncertain")
	if _, err := db.CloseRegisteredWork(ctx, closeInput(r)); err == nil {
		t.Fatal("preparing closed")
	}
	r, err := db.MarkWorkUncertain(ctx, workMutation(r))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CloseRegisteredWork(ctx, closeInput(r)); err == nil {
		t.Fatal("uncertain closed")
	}
	before := lifecycleContents(t, db)
	if _, _, err = db.BeginWorkPreparation(ctx, workMutation(r)); err == nil {
		t.Fatal("uncertain resumed")
	}
	assertLifecycleUnchanged(t, db, before)
	lifecycleSQL(t, db, `UPDATE work_admissions SET revision=? WHERE id=?`, int64(math.MaxInt64), r.ID)
	r.Revision = math.MaxInt64
	if _, err = db.MarkWorkUncertain(ctx, workMutation(r)); err == nil {
		t.Fatal("overflow")
	}
	lifecycleSQL(t, db, `DELETE FROM work_admission_targets WHERE admission_id=? AND tenant_id='other'`, r.ID)
	if _, err = db.GetWorkAdmission(ctx, r.ID); err != ErrWorkAdmissionCorrupt {
		t.Fatalf("missing targets: %v", err)
	}
}
func TestWorkAdmissionOwnerNeverSerialized(t *testing.T) {
	db, _ := lifecycleFixture(t)
	r := admitFixture(t, db, "secret")
	m := workMutation(r)
	raw, _ := json.Marshal(closeInput(r))
	if strings.Contains(string(raw), m.OwnerToken) {
		t.Fatal("owner token serialized")
	}
	before := lifecycleContents(t, db)
	got, err := db.GetWorkAdmission(context.Background(), r.ID)
	if err != nil || !reflect.DeepEqual(r, got) {
		t.Fatalf("read %v", err)
	}
	assertLifecycleUnchanged(t, db, before)
}
