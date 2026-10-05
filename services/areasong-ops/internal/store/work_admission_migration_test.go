package store

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"path/filepath"
	"reflect"
	"testing"
)

func workOldSchema(t *testing.T, version int) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	path := ""
	if err = db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:version] {
		if _, err = db.Exec(migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, version)); err != nil {
		t.Fatal(err)
	}
	return &Store{db: db, path: path}
}
func TestWorkAdmissionMigration49AndReopen(t *testing.T) {
	for _, version := range []int{47, 48} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			old := workOldSchema(t, version)
			seedLifecycleHistory(t, old)
			before := lifecycleContents(t, old)
			path := old.path
			old.Close()
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			after := lifecycleContents(t, db)
			for table, value := range before {
				if table != "user_version" && after[table] != value {
					t.Fatalf("history changed %s", table)
				}
			}
			if after["user_version"] != "50" || after["work_admissions"] != "null" || after["work_admission_targets"] != "null" {
				t.Fatalf("new tables not empty: %+v", after)
			}
			again := secondWorkStore(t, db)
			assertLifecycleUnchanged(t, again, after)
		})
	}
}
func TestWorkAdmissionMigration49RollsBackOnly49(t *testing.T) {
	for _, collision := range []string{"work_admission_targets", "idx_work_admission_targets_tenant"} {
		t.Run(collision, func(t *testing.T) {
			old := workOldSchema(t, 48)
			seedLifecycleHistory(t, old)
			lifecycleSQL(t, old, `CREATE TABLE `+collision+`(sentinel TEXT)`)
			lifecycleSQL(t, old, `INSERT INTO `+collision+` VALUES('preserve')`)
			before := lifecycleContents(t, old)
			path := old.path
			old.Close()
			if db, err := Open(path); err == nil {
				db.Close()
				t.Fatal("migration collision accepted")
			}
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			check := &Store{db: raw}
			if !reflect.DeepEqual(before, lifecycleContents(t, check)) {
				t.Fatal("failed49 left partial tables or changed history")
			}
			var version int
			if err = raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 48 {
				t.Fatalf("version %d %v", version, err)
			}
		})
	}
}
func TestWorkAdmissionReopenRetainsBlockers(t *testing.T) {
	db, _ := lifecycleFixture(t)
	ctx := context.Background()
	values := []model.WorkAdmission{admitFixture(t, db, "admitted"), prepareFixture(t, db, "preparing"), bindFixture(t, db, prepareFixture(t, db, "bound"))}
	uncertain, err := db.MarkWorkUncertain(ctx, workMutation(admitFixture(t, db, "uncertain")))
	if err != nil {
		t.Fatal(err)
	}
	values = append(values, uncertain)
	execution := approveLifecycle(t, db, lifecycleRequest(1, 1, "disabled"))
	before := lifecycleContents(t, db)
	path := db.path
	db.Close()
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertLifecycleUnchanged(t, reopened, before)
	for _, want := range values {
		got, err := reopened.GetWorkAdmission(ctx, want.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("restart altered record: %+v %v", got, err)
		}
	}
	if _, err = reopened.applyTenantLifecycleChange(ctx, execution); err != ErrUnfinishedRegisteredWork {
		t.Fatalf("restart released blocker: %v", err)
	}
	assertLifecycleUnchanged(t, reopened, before)
}
func TestWorkAdmissionOldWorkIsNotBackfilled(t *testing.T) {
	db, _ := lifecycleFixture(t)
	ctx := context.Background()
	input := workInput("old")
	record := model.WorkAdmission{Request: input.Request}
	tx, err := db.beginRegisteredWorkTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = insertWorkTask(ctx, tx.Tx, record, "legacy-unregistered"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = db.db.QueryRow(`SELECT count(*) FROM work_admissions`).Scan(&n); err != nil || n != 0 {
		t.Fatal("fabricated registration")
	}
	before := lifecycleContents(t, db)
	second := secondWorkStore(t, db)
	assertLifecycleUnchanged(t, second, before)
	// 私有基元只检查已登记工作，旧任务不被伪造为已结束，也不开放外部入口。
	tx, err = second.beginRegisteredWorkTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = assertNoUnfinishedRegisteredWorkTx(ctx, tx.Tx, "team"); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = tx.QueryRow(`SELECT state FROM tasks WHERE id='legacy-unregistered'`).Scan(&state); err != nil || state != "queued" {
		t.Fatalf("legacy task changed: %s %v", state, err)
	}
}
func TestWorkAdmissionSchemaConstraints(t *testing.T) {
	db, _ := lifecycleFixture(t)
	r := admitFixture(t, db, "constraints")
	for _, sql := range []string{
		`UPDATE work_admissions SET revision=0`, `UPDATE work_admissions SET revision=1.5`, `UPDATE work_admissions SET state='unknown'`,
		`UPDATE work_admissions SET state='closed'`, `UPDATE work_admissions SET state='task_bound'`, `UPDATE work_admissions SET task_id='absent'`,
		`UPDATE work_admission_targets SET expected_generation=0`, `UPDATE work_admission_targets SET expected_generation=1.5`,
		`UPDATE work_admission_targets SET admission_id='absent'`, `UPDATE work_admission_targets SET tenant_id='Team'`,
		`DELETE FROM work_admissions`,
	} {
		before := lifecycleContents(t, db)
		if _, err := db.db.Exec(sql); err == nil {
			t.Fatalf("constraint accepted: %s", sql)
		}
		assertLifecycleUnchanged(t, db, before)
	}
	if _, err := db.GetWorkAdmission(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
}
