package store

import (
	"context"
	"errors"
	"fmt"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func secondWorkStore(t *testing.T, first *Store) *Store {
	t.Helper()
	second, err := Open(first.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	lifecycleSQL(t, second, `PRAGMA busy_timeout=0`)
	return second
}

// now位于首写锁之后、业务写入之前；仅暂停首个调用，通道控制顺序。
func holdWorkWriter(t *testing.T, first *Store, run func() error) func() error {
	t.Helper()
	arrived := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	var once sync.Once
	first.now = func() time.Time { once.Do(func() { close(arrived); <-release }); return time.Now().UTC() }
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	go func() { done <- run() }()
	select {
	case <-arrived:
	case err := <-done:
		t.Fatalf("writer did not reach locked barrier: %v", err)
	case <-time.After(10 * time.Second):
		unblock()
		t.Fatal("writer barrier timeout")
	}
	return func() error {
		unblock()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			return fmt.Errorf("writer completion timeout")
		}
	}
}
func requireWorkBusy(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "SQLITE_BUSY") {
		t.Fatalf("expected independent writer exclusion, got %v", err)
	}
}
func TestWorkAdmissionZeroRowWriteLock(t *testing.T) {
	first, _ := lifecycleFixture(t)
	second := secondWorkStore(t, first)
	before := lifecycleContents(t, first)
	tx, err := first.beginRegisteredWorkTx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = second.db.Exec(`UPDATE tenants SET lifecycle_generation=lifecycle_generation WHERE id='team'`)
	requireWorkBusy(t, err)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	lifecycleSQL(t, second, `UPDATE tenants SET lifecycle_generation=lifecycle_generation WHERE id='team'`)
	assertLifecycleUnchanged(t, first, before)
}
func TestWorkAdmissionTwoStoreLifecycleOrdering(t *testing.T) {
	for _, admitFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(admitFirst), func(t *testing.T) {
			first, _ := lifecycleFixture(t)
			execution := approveLifecycle(t, first, lifecycleRequest(1, 1, "disabled"))
			second := secondWorkStore(t, first)
			ctx := context.Background()
			finish := holdWorkWriter(t, first, func() error {
				if admitFirst {
					_, _, err := first.AdmitWork(ctx, workInput("race"))
					return err
				}
				_, err := first.applyTenantLifecycleChange(ctx, execution)
				return err
			})
			if admitFirst {
				_, err := second.applyTenantLifecycleChange(ctx, execution)
				requireWorkBusy(t, err)
			} else {
				_, _, err := second.AdmitWork(ctx, workInput("race"))
				requireWorkBusy(t, err)
			}
			if err := finish(); err != nil {
				t.Fatal(err)
			}
			before := lifecycleContents(t, first)
			if admitFirst {
				_, err := second.applyTenantLifecycleChange(ctx, execution)
				if !errors.Is(err, ErrUnfinishedRegisteredWork) {
					t.Fatalf("registered work not blocking: %v", err)
				}
			} else {
				_, created, err := second.AdmitWork(ctx, workInput("race"))
				if err == nil || created {
					t.Fatal("disabled admitted")
				}
			}
			assertLifecycleUnchanged(t, first, before)
		})
	}
}
func TestWorkAdmissionConcurrentRequestAndPreparation(t *testing.T) {
	first, _ := lifecycleFixture(t)
	second := secondWorkStore(t, first)
	ctx := context.Background()
	finish := holdWorkWriter(t, first, func() error {
		_, created, err := first.AdmitWork(ctx, workInput("same"))
		if err == nil && !created {
			return fmt.Errorf("first not created")
		}
		return err
	})
	_, created, err := second.AdmitWork(ctx, workInput("same"))
	requireWorkBusy(t, err)
	if created {
		t.Fatal("duplicate admission")
	}
	if err = finish(); err != nil {
		t.Fatal(err)
	}
	record, created, err := second.AdmitWork(ctx, workInput("same"))
	if err != nil || created {
		t.Fatalf("replay: %v", err)
	}
	m := workMutation(record)
	finish = holdWorkWriter(t, first, func() error {
		_, advanced, err := first.BeginWorkPreparation(ctx, m)
		if err == nil && !advanced {
			return fmt.Errorf("first not advanced")
		}
		return err
	})
	_, advanced, err := second.BeginWorkPreparation(ctx, m)
	requireWorkBusy(t, err)
	if advanced {
		t.Fatal("duplicate execution right")
	}
	if err = finish(); err != nil {
		t.Fatal(err)
	}
	before := lifecycleContents(t, first)
	if _, advanced, err = second.BeginWorkPreparation(ctx, m); err == nil || advanced {
		t.Fatal("late retry advanced")
	}
	assertLifecycleUnchanged(t, first, before)
	var count int
	if err = first.db.QueryRow(`SELECT count(*) FROM work_admissions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count %d %v", count, err)
	}
}
func TestWorkAdmissionCloseVersusPreparation(t *testing.T) {
	for _, closeFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(closeFirst), func(t *testing.T) {
			first, _ := lifecycleFixture(t)
			r := admitFixture(t, first, "close-race")
			second := secondWorkStore(t, first)
			ctx := context.Background()
			finish := holdWorkWriter(t, first, func() error {
				if closeFirst {
					_, err := first.CloseRegisteredWork(ctx, closeInput(r))
					return err
				}
				_, _, err := first.BeginWorkPreparation(ctx, workMutation(r))
				return err
			})
			if closeFirst {
				_, _, err := second.BeginWorkPreparation(ctx, workMutation(r))
				requireWorkBusy(t, err)
			} else {
				_, err := second.CloseRegisteredWork(ctx, closeInput(r))
				requireWorkBusy(t, err)
			}
			if err := finish(); err != nil {
				t.Fatal(err)
			}
			before := lifecycleContents(t, first)
			if closeFirst {
				if _, advanced, err := second.BeginWorkPreparation(ctx, workMutation(r)); err == nil || advanced {
					t.Fatal("closed resumed")
				}
			} else {
				if _, err := second.CloseRegisteredWork(ctx, closeInput(r)); err == nil {
					t.Fatal("preparing closed")
				}
			}
			assertLifecycleUnchanged(t, first, before)
		})
	}
}
func TestWorkAdmissionWriteFailuresRollback(t *testing.T) {
	triggers := map[string]string{
		"admission":     "BEFORE INSERT ON work_admissions",
		"target-first":  "BEFORE INSERT ON work_admission_targets WHEN NEW.tenant_id='other'",
		"target-second": "BEFORE INSERT ON work_admission_targets WHEN NEW.tenant_id='team'",
		"preparing":     "BEFORE UPDATE ON work_admissions WHEN NEW.state='preparing'",
		"uncertain":     "BEFORE UPDATE ON work_admissions WHEN NEW.state='uncertain'",
		"close":         "BEFORE UPDATE ON work_admissions WHEN NEW.state='closed'",
		"task":          "BEFORE INSERT ON tasks",
		"bind":          "BEFORE UPDATE ON work_admissions WHEN NEW.state='task_bound'",
	}
	for name, trigger := range triggers {
		t.Run(name, func(t *testing.T) {
			db, _ := lifecycleFixture(t)
			ctx := context.Background()
			var r model.WorkAdmission
			switch name {
			case "preparing", "uncertain", "close":
				r = admitFixture(t, db, name)
			case "task", "bind":
				r = prepareFixture(t, db, name)
			}
			lifecycleSQL(t, db, `CREATE TRIGGER work_fault `+trigger+` BEGIN SELECT RAISE(ABORT,'synthetic write failure'); END`)
			before := lifecycleContents(t, db)
			var err error
			switch name {
			case "preparing":
				_, _, err = db.BeginWorkPreparation(ctx, workMutation(r))
			case "uncertain":
				_, err = db.MarkWorkUncertain(ctx, workMutation(r))
			case "close":
				_, err = db.CloseRegisteredWork(ctx, closeInput(r))
			case "task", "bind":
				tx, e := db.beginRegisteredWorkTx(ctx)
				if e != nil {
					t.Fatal(e)
				}
				err = insertWorkTask(ctx, tx.Tx, r, "fault-task")
				if err == nil {
					_, err = db.bindRegisteredWorkTaskTx(ctx, tx.Tx, workMutation(r), "fault-task")
				}
				tx.Rollback()
			default:
				_, _, err = db.AdmitWork(ctx, workInput(name))
			}
			if err == nil {
				t.Fatal("fault accepted")
			}
			assertLifecycleUnchanged(t, db, before)
			reopened := secondWorkStore(t, db)
			assertLifecycleUnchanged(t, reopened, before)
		})
	}
}
func TestWorkAdmissionCommitFailureNoExecutionRight(t *testing.T) {
	db, _ := lifecycleFixture(t)
	r := admitFixture(t, db, "commit")
	ctx := context.Background()
	lifecycleSQL(t, db, `CREATE TABLE work_commit_fault(ref TEXT REFERENCES work_admissions(id) DEFERRABLE INITIALLY DEFERRED)`)
	lifecycleSQL(t, db, `CREATE TRIGGER fail_work_commit AFTER UPDATE ON work_admissions BEGIN INSERT INTO work_commit_fault VALUES('absent'); END`)
	before := lifecycleContents(t, db)
	_, advanced, err := db.BeginWorkPreparation(ctx, workMutation(r))
	if err == nil || advanced {
		t.Fatalf("commit failure granted right: %v", err)
	}
	// 提交失败后原Store可重用；失败连接不得把未提交事务带回池中。
	assertLifecycleUnchanged(t, db, before)
	reopened, err := Open(db.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.GetWorkAdmission(ctx, r.ID)
	if err != nil || got.State != model.WorkAdmitted || got.Revision != 1 {
		t.Fatalf("partial commit: %+v %v", got, err)
	}
}
func TestWorkAdmissionBindingRejectsChangedTaskAndTargets(t *testing.T) {
	for _, field := range []string{"actor_hash", "plan_id", "plan_digest", "request_hash", "idempotency_key", "state", "generation"} {
		t.Run(field, func(t *testing.T) {
			db, _ := lifecycleFixture(t)
			ctx := context.Background()
			r := prepareFixture(t, db, "bind-guard")
			before := lifecycleContents(t, db)
			tx, err := db.beginRegisteredWorkTx(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err = insertWorkTask(ctx, tx.Tx, r, "bind-task"); err != nil {
				t.Fatal(err)
			}
			if field == "generation" {
				_, err = tx.Exec(`UPDATE tenants SET lifecycle_generation=2 WHERE id='team'`)
			} else {
				_, err = tx.Exec(`UPDATE tasks SET ` + field + `='wrong' WHERE id='bind-task'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.bindRegisteredWorkTaskTx(ctx, tx.Tx, workMutation(r), "bind-task"); err == nil {
				t.Fatal("bind accepted")
			}
			tx.Rollback()
			if !reflect.DeepEqual(before, lifecycleContents(t, db)) {
				t.Fatal("partial binding")
			}
		})
	}
}
