package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"testing"
)

func TestPlanPreparationWriteFailuresRollback(t *testing.T) {
	for _, point := range []string{"admission", "target", "begin", "result", "plan", "audit", "close", "commit"} {
		t.Run(point, func(t *testing.T) {
			db, _ := lifecycleFixture(t)
			ctx := context.Background()
			input := planPreparationInput(t, db, "fault-"+point)
			var r model.PlanPreparation
			var plan ReleasePlanInput
			var err error
			switch point {
			case "begin", "result", "commit":
				r, _, err = db.AdmitPlanPreparation(ctx, input)
				if err != nil {
					t.Fatal(err)
				}
				if point == "result" {
					r, _, err = db.BeginPlanInspection(ctx, preparationOwner(r))
					if err != nil {
						t.Fatal(err)
					}
				}
			case "plan", "audit", "close":
				r, plan = inspectedPreparation(t, db, "fault-"+point)
			}
			tables := map[string]string{"admission": "INSERT ON release_plan_preparations", "target": "INSERT ON release_plan_preparation_targets",
				"begin": "UPDATE ON release_plan_preparations", "result": "UPDATE ON release_plan_preparations", "plan": "INSERT ON release_plans",
				"audit": "INSERT ON audit_entries", "close": "UPDATE ON release_plan_preparations"}
			if point == "commit" {
				lifecycleSQL(t, db, `CREATE TABLE preparation_commit_fault(ref TEXT REFERENCES release_plan_preparations(id) DEFERRABLE INITIALLY DEFERRED)`)
				lifecycleSQL(t, db, `CREATE TRIGGER preparation_fault AFTER UPDATE ON release_plan_preparations BEGIN INSERT INTO preparation_commit_fault VALUES('absent'); END`)
			} else {
				lifecycleSQL(t, db, "CREATE TRIGGER preparation_fault BEFORE "+tables[point]+" BEGIN SELECT RAISE(ABORT,'synthetic failure'); END")
			}
			before := lifecycleContents(t, db)
			switch point {
			case "admission", "target":
				_, _, err = db.AdmitPlanPreparation(ctx, input)
			case "begin", "commit":
				var advanced bool
				_, advanced, err = db.BeginPlanInspection(ctx, preparationOwner(r))
				if advanced {
					t.Fatal("失败提交授予检查权")
				}
			case "result":
				_, err = db.RecordPlanInspectionDone(ctx, preparationOwner(r), model.PlanInspectionResult{Kind: "inspection_settled_v1", ScopeDigest: r.Request.Binding.ScopeDigest, Failure: "safe failure", CleanupDigest: input.Request.InputDigest, CallDigests: []string{}})
			default:
				_, err = db.FinishPreparedReleasePlan(ctx, preparationOwner(r), &plan)
			}
			if err == nil {
				t.Fatal("注入未生效")
			}
			assertLifecycleUnchanged(t, db, before)
			other := secondWorkStore(t, db)
			assertLifecycleUnchanged(t, other, before)
		})
	}
}

func TestPlanPreparationTwoConnectionLifecycleAndOnce(t *testing.T) {
	for _, firstKind := range []string{"preparation", "disable"} {
		t.Run(firstKind, func(t *testing.T) {
			first, _ := lifecycleFixture(t)
			second := secondWorkStore(t, first)
			ctx := context.Background()
			input := planPreparationInput(t, first, "race")
			execution := approveLifecycle(t, first, lifecycleRequest(1, 1, "disabled"))
			finish := holdWorkWriter(t, first, func() error {
				if firstKind == "preparation" {
					_, _, e := first.AdmitPlanPreparation(ctx, input)
					return e
				}
				_, e := first.applyTenantLifecycleChange(ctx, execution)
				return e
			})
			var err error
			if firstKind == "preparation" {
				_, err = second.applyTenantLifecycleChange(ctx, execution)
			} else {
				_, _, err = second.AdmitPlanPreparation(ctx, input)
			}
			requireWorkBusy(t, err)
			if err = finish(); err != nil {
				t.Fatal(err)
			}
			if firstKind == "preparation" {
				_, err = second.applyTenantLifecycleChange(ctx, execution)
				if !errors.Is(err, ErrUnfinishedRegisteredWork) {
					t.Fatal(err)
				}
			} else {
				if _, created, e := second.AdmitPlanPreparation(ctx, input); e == nil || created {
					t.Fatal("停用后获准")
				}
			}
		})
	}
	first, _ := lifecycleFixture(t)
	second := secondWorkStore(t, first)
	ctx := context.Background()
	r, _, err := first.AdmitPlanPreparation(ctx, planPreparationInput(t, first, "once"))
	if err != nil {
		t.Fatal(err)
	}
	m := preparationOwner(r)
	finish := holdWorkWriter(t, first, func() error {
		_, advanced, e := first.BeginPlanInspection(ctx, m)
		if e == nil && !advanced {
			return fmt.Errorf("首次未取得权")
		}
		return e
	})
	_, advanced, err := second.BeginPlanInspection(ctx, m)
	requireWorkBusy(t, err)
	if advanced {
		t.Fatal("并发双权")
	}
	if err = finish(); err != nil {
		t.Fatal(err)
	}
	if _, advanced, err = second.BeginPlanInspection(ctx, m); err == nil || advanced {
		t.Fatal("提交后重放双权")
	}
}

func TestPlanPreparationMigration50FailurePreserves49(t *testing.T) {
	for _, name := range []string{"release_plan_preparations", "release_plan_preparation_targets", "idx_release_plan_preparation_targets_tenant"} {
		t.Run(name, func(t *testing.T) {
			old := workOldSchema(t, 49)
			if name == "idx_release_plan_preparation_targets_tenant" {
				lifecycleSQL(t, old, "CREATE TABLE collision(id TEXT)")
				lifecycleSQL(t, old, "CREATE INDEX "+name+" ON collision(id)")
			} else {
				lifecycleSQL(t, old, "CREATE TABLE "+name+"(id TEXT)")
			}
			before := lifecycleContents(t, old)
			path := old.path
			old.Close()
			opened, err := Open(path)
			if err == nil {
				opened.Close()
				t.Fatal("50冲突被掩盖")
			}
			raw := workOldSchemaRead(t, path)
			defer raw.Close()
			assertLifecycleUnchanged(t, raw, before)
		})
	}
}

func workOldSchemaRead(t *testing.T, path string) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return &Store{db: db, path: path}
}

func TestPlanPreparationNoWorkCloseCompetesWithInspection(t *testing.T) {
	for _, closeFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(closeFirst), func(t *testing.T) {
			first, _ := lifecycleFixture(t)
			second := secondWorkStore(t, first)
			ctx := context.Background()
			record, _, err := first.AdmitPlanPreparation(ctx, planPreparationInput(t, first, "close-race"))
			if err != nil {
				t.Fatal(err)
			}
			mutation := preparationOwner(record)
			finish := holdWorkWriter(t, first, func() error {
				if closeFirst {
					_, e := first.ClosePlanPreparationNoWork(ctx, mutation)
					return e
				}
				_, _, e := first.BeginPlanInspection(ctx, mutation)
				return e
			})
			if closeFirst {
				_, _, err = second.BeginPlanInspection(ctx, mutation)
			} else {
				_, err = second.ClosePlanPreparationNoWork(ctx, mutation)
			}
			requireWorkBusy(t, err)
			if err = finish(); err != nil {
				t.Fatal(err)
			}
			before := lifecycleContents(t, first)
			if closeFirst {
				if _, advanced, e := second.BeginPlanInspection(ctx, mutation); e == nil || advanced {
					t.Fatal("关闭后获执行权")
				}
			} else {
				if _, e := second.ClosePlanPreparationNoWork(ctx, mutation); e == nil {
					t.Fatal("已准备被关闭")
				}
			}
			assertLifecycleUnchanged(t, first, before)
		})
	}
}

func TestPlanPreparationPermissionRevocationOrdering(t *testing.T) {
	for _, order := range []string{"revoke-before-admit", "admit-before-revoke", "revoke-before-finish", "finish-before-revoke"} {
		t.Run(order, func(t *testing.T) {
			first, policy := lifecycleFixture(t)
			second := secondWorkStore(t, first)
			ctx := context.Background()
			admin := policy.Principals[lifecycleApprover]
			admin.Roles = []string{"platform-admin"}
			policy.Principals[lifecycleApprover] = admin
			if _, err := first.SaveAccessPolicySnapshot(ctx, lifecycleSnapshot(t, policy), 1); err != nil {
				t.Fatal(err)
			}
			input := planPreparationInput(t, first, order)
			revoked := policy.Principals[lifecycleActor]
			revoked.Status = "disabled"
			policy.Principals[lifecycleActor] = revoked
			snapshot := lifecycleSnapshot(t, policy)
			snapshot.ActorHash = lifecycleApprover
			revoke := func() error { _, err := second.SaveAccessPolicySnapshot(ctx, snapshot, 2); return err }
			if order == "revoke-before-admit" {
				if err := revoke(); err != nil {
					t.Fatal(err)
				}
				before := lifecycleContents(t, first)
				if _, created, err := first.AdmitPlanPreparation(ctx, input); err == nil || created {
					t.Fatal("捕获权限后撤权仍准入")
				}
				assertLifecycleUnchanged(t, first, before)
				return
			}
			if order == "admit-before-revoke" {
				finish := holdWorkWriter(t, first, func() error { _, _, err := first.AdmitPlanPreparation(ctx, input); return err })
				requireWorkBusy(t, revoke())
				if err := finish(); err != nil {
					t.Fatal(err)
				}
				if err := revoke(); err != nil {
					t.Fatal(err)
				}
				record, err := first.GetPlanPreparation(ctx, input.ID)
				if err != nil {
					t.Fatal(err)
				}
				before := lifecycleContents(t, first)
				if _, advanced, err := first.BeginPlanInspection(ctx, preparationOwner(record)); err == nil || advanced {
					t.Fatal("撤权后获检查权")
				}
				assertLifecycleUnchanged(t, first, before)
				return
			}
			record, plan := inspectedPreparation(t, first, order)
			if order == "revoke-before-finish" {
				if err := revoke(); err != nil {
					t.Fatal(err)
				}
				before := lifecycleContents(t, first)
				if _, err := first.FinishPreparedReleasePlan(ctx, preparationOwner(record), &plan); err == nil {
					t.Fatal("检查后撤权仍提交计划")
				}
				assertLifecycleUnchanged(t, first, before)
				return
			}
			finish := holdWorkWriter(t, first, func() error {
				_, err := first.FinishPreparedReleasePlan(ctx, preparationOwner(record), &plan)
				return err
			})
			requireWorkBusy(t, revoke())
			if err := finish(); err != nil {
				t.Fatal(err)
			}
			before, err := first.GetReleasePlan(ctx, plan.Plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := revoke(); err != nil {
				t.Fatal(err)
			}
			after, err := first.GetReleasePlan(ctx, plan.Plan.ID)
			if err != nil || !sameReleaseJSON(before, after) {
				t.Fatal("撤权伪造已经提交的历史", err)
			}
		})
	}
}
