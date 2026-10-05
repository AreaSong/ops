package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func syntheticSchema47(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic47.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	for i, migration := range migrations[:47] {
		if _, err := db.Exec(migration); err != nil {
			t.Fatalf("migration %d: %v", i+1, err)
		}
	}
	if _, err := db.Exec(`PRAGMA user_version=47`); err != nil {
		t.Fatal(err)
	}
	return &Store{db: db, path: path, now: func() time.Time { return time.Now().UTC() }}
}

func seedLifecycle47(t *testing.T, db *Store) config.AccessPolicy {
	t.Helper()
	policy := config.AccessPolicy{DefaultTenant: "default", Tenants: map[string]model.Tenant{}}
	for _, item := range []struct{ id, status, source string }{
		{"default", "active", "bootstrap"}, {"bootstrap", "", "bootstrap"}, {"ordinary", "active", lifecycleActor},
		{"empty", "", lifecycleActor}, {"different-editor", "active", lifecycleActor}, {"disabled", "disabled", lifecycleActor},
		{"other", "other", lifecycleActor}, {"upper-status", "ACTIVE", lifecycleActor}, {"space-status", " active ", lifecycleActor},
		{"no-source", "active", ""}, {"bad-source", "active", "unknown"}, {"upper-source", "active", strings.Repeat("A", 64)},
		{"alias", "active", lifecycleActor}, {"ALIAS", "active", lifecycleActor}, {"space ", "active", lifecycleActor},
		{"mismatch", "active", lifecycleActor}, {"missing", "active", lifecycleActor}, {"wrong-id", "active", lifecycleActor},
		{"row-bootstrap", "active", "bootstrap"},
	} {
		lifecycleSQL(t, db, `INSERT INTO tenants(id,display_name,status,created_by,created_at,updated_at) VALUES(?,?,?,?,?,?)`, item.id, item.id, item.status, item.source, "2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z")
		policy.Tenants[item.id] = model.Tenant{ID: item.id, DisplayName: item.id, Status: item.status, CreatedBy: item.source}
	}
	for id, alter := range map[string]func(*model.Tenant){
		"default": func(v *model.Tenant) { v.CreatedBy = "" }, "bootstrap": func(v *model.Tenant) { v.CreatedBy = "" },
		"different-editor": func(v *model.Tenant) { v.CreatedBy = lifecycleApprover },
		"mismatch":         func(v *model.Tenant) { v.DisplayName = "drift" },
		"wrong-id":         func(v *model.Tenant) { v.ID = "other-id" },
		"row-bootstrap":    func(v *model.Tenant) { v.CreatedBy = lifecycleActor },
	} {
		value := policy.Tenants[id]
		alter(&value)
		policy.Tenants[id] = value
	}
	delete(policy.Tenants, "missing")
	role := model.Role{ID: "legacy-viewer", DisplayName: "Legacy", Permissions: []model.Permission{model.PermissionRead}, CreatedBy: lifecycleActor}
	if err := db.UpsertRole(context.Background(), role); err != nil {
		t.Fatal(err)
	}
	binding := model.RoleBinding{ID: "legacy-binding", Subject: lifecycleApprover, TenantID: "ordinary", RoleID: role.ID, ObjectIDs: []string{"service:synthetic"}, CreatedBy: lifecycleActor}
	if err := db.UpsertRoleBinding(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	policy.Roles = map[string]model.Role{role.ID: role}
	policy.Bindings = []model.RoleBinding{binding}
	raw := lifecycleSnapshot(t, policy)
	lifecycleSQL(t, db, `INSERT INTO access_policy_snapshots(version,digest,policy_json,actor_hash,created_at) VALUES(1,?,?,?,?)`, raw.Digest, raw.PolicyJSON, lifecycleActor, "2026-01-01T00:00:00Z")
	// 原始历史载荷/摘要均无代次，不允许迁移补造。
	lifecycleSQL(t, db, `INSERT INTO release_plans(id,actor_hash,service,action,target,risk,state,digest,approval_summary_json,confirmation_hash,created_at,updated_at)
		VALUES('historical-plan',?,'synthetic','inspect','','low','approved','historical-digest','{"tenantId":"ordinary"}','confirmation','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, lifecycleActor)
	_, _, err := db.CreateAccessChange(context.Background(), model.AccessChange{ID: "historical-change", IdempotencyKey: "historical-key", ActorHash: lifecycleActor,
		RequestDigest: digestPolicyJSON(`{"idempotencyKey":"historical-key"}`), RequiresDualApproval: true, ApprovalPolicy: model.ApprovalPolicyTwoParty}, `{"idempotencyKey":"historical-key"}`, "confirmation")
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestTenantLifecycleMigration47Conservative(t *testing.T) {
	old := syntheticSchema47(t)
	seedLifecycle47(t, old)
	before := lifecycleContents(t, old)
	old.Close()
	db, err := Open(old.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	after := lifecycleContents(t, db)
	for table, value := range before {
		if table != "tenants" && table != "user_version" && after[table] != value {
			t.Fatal("historical table changed", table)
		}
	}
	if after["user_version"] != "50" {
		t.Fatal(after["user_version"])
	}
	var tuples []string
	if err := json.Unmarshal([]byte(after["tenants"]), &tuples); err != nil {
		t.Fatal(err)
	}
	var stripped []string
	known := map[string]bool{"default": true, "bootstrap": true, "ordinary": true, "empty": true, "different-editor": true}
	for _, tuple := range tuples {
		var values []any
		if err := json.Unmarshal([]byte(tuple), &values); err != nil {
			t.Fatal(err)
		}
		id := values[0].(string)
		generation := values[len(values)-1].(float64)
		want := float64(0)
		if known[id] {
			want = 1
		}
		if generation != want {
			t.Fatalf("%s generation=%v want=%v", id, generation, want)
		}
		raw, _ := json.Marshal(values[:len(values)-1])
		stripped = append(stripped, string(raw))
	}
	sort.Strings(stripped)
	raw, _ := json.Marshal(stripped)
	if string(raw) != before["tenants"] {
		t.Fatal("original tenant columns changed")
	}
	// 重复 seed / active+0 改名只保留代次，不能把未知提升成已知。
	if err := db.EnsureAccessDefaults(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertTenant(context.Background(), model.Tenant{ID: "no-source", DisplayName: "Renamed", Status: "active", CreatedBy: lifecycleActor}); err != nil {
		t.Fatal(err)
	}
	var zero int
	if err := db.db.QueryRow(`SELECT lifecycle_generation FROM tenants WHERE id='no-source'`).Scan(&zero); err != nil || zero != 0 {
		t.Fatal(zero, err)
	}
	final := lifecycleContents(t, db)
	db.Close()
	reopened, err := Open(old.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertLifecycleUnchanged(t, reopened, final)
}

func TestTenantLifecycleMigrationUnknownSnapshot(t *testing.T) {
	for _, kind := range []string{"missing", "digest", "malformed", "unknown-field", "duplicate-key"} {
		t.Run(kind, func(t *testing.T) {
			old := syntheticSchema47(t)
			seedLifecycle47(t, old)
			switch kind {
			case "missing":
				lifecycleSQL(t, old, `DELETE FROM access_policy_snapshots WHERE version=1`)
			case "digest":
				lifecycleSQL(t, old, `UPDATE access_policy_snapshots SET digest='bad' WHERE version=1`)
			default:
				var raw string
				if err := old.db.QueryRow(`SELECT policy_json FROM access_policy_snapshots WHERE version=1`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if kind == "malformed" {
					raw = "{"
				} else if kind == "unknown-field" {
					raw = strings.TrimSuffix(raw, "}") + `,"unknown":true}`
				} else {
					raw = strings.Replace(raw, `"enforced":false`, `"enforced":true,"enforced":false`, 1)
				}
				lifecycleSQL(t, old, `UPDATE access_policy_snapshots SET policy_json=?,digest=? WHERE version=1`, raw, digestPolicyJSON(raw))
			}
			before := lifecycleContents(t, old)
			old.Close()
			db, err := Open(old.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var count int
			if err := db.db.QueryRow(`SELECT COUNT(*) FROM tenants WHERE lifecycle_generation!=0`).Scan(&count); err != nil || count != 0 {
				t.Fatal(count, err)
			}
			after := lifecycleContents(t, db)
			for table, value := range before {
				if table != "tenants" && table != "user_version" && after[table] != value {
					t.Fatal("changed", table)
				}
			}
		})
	}
}

func TestTenantLifecycleMigrationFailureRollback(t *testing.T) {
	old := syntheticSchema47(t)
	seedLifecycle47(t, old)
	lifecycleSQL(t, old, `CREATE TRIGGER init_failure BEFORE UPDATE ON tenants BEGIN SELECT RAISE(ABORT,'synthetic init failure'); END`)
	before := lifecycleContents(t, old)
	old.Close()
	if db, err := Open(old.path); err == nil {
		db.Close()
		t.Fatal("migration unexpectedly succeeded")
	}
	conn, err := sql.Open("sqlite", old.path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	check := &Store{db: conn}
	assertLifecycleUnchanged(t, check, before)
	var columns int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('tenants') WHERE name='lifecycle_generation'`).Scan(&columns); err != nil || columns != 0 {
		t.Fatal(columns, err)
	}
	lifecycleSQL(t, check, `DROP TRIGGER init_failure`)
	conn.Close()
	db, err := Open(old.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 50 {
		t.Fatal(version, err)
	}
}

func TestTenantLifecycleSchemaTypeAndFutureVersion(t *testing.T) {
	db, _ := lifecycleFixture(t)
	var typ, defaultValue string
	var notnull int
	if err := db.db.QueryRow(`SELECT type,"notnull",dflt_value FROM pragma_table_info('tenants') WHERE name='lifecycle_generation'`).Scan(&typ, &notnull, &defaultValue); err != nil {
		t.Fatal(err)
	}
	if typ != "INTEGER" || notnull != 1 || defaultValue != "0" || CurrentSchemaVersion() != 50 {
		t.Fatalf("%s %d %s", typ, notnull, defaultValue)
	}
	for _, value := range []any{-1, 1.5, "invalid", nil} {
		before := lifecycleContents(t, db)
		if _, err := db.db.Exec(`UPDATE tenants SET lifecycle_generation=? WHERE id='team'`, value); err == nil {
			t.Fatalf("invalid generation accepted: %v", value)
		}
		assertLifecycleUnchanged(t, db, before)
	}
	lifecycleSQL(t, db, `PRAGMA user_version=51`)
	before := lifecycleContents(t, db)
	path := db.path
	db.Close()
	if other, err := Open(path); err == nil {
		other.Close()
		t.Fatal("future schema accepted")
	}
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	assertLifecycleUnchanged(t, &Store{db: conn}, before)
}

func TestTenantLifecycleMigrationEmptyDatabase(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tenants, version int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM tenants`).Scan(&tenants); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if tenants != 0 || version != 50 {
		t.Fatal(tenants, version)
	}
	before := lifecycleContents(t, db)
	if _, err := db.GetTenantLifecycle(context.Background(), "none"); err == nil {
		t.Fatal("missing snapshot accepted")
	}
	assertLifecycleUnchanged(t, db, before)
}

func TestTenantLifecycleSeedKeepsUnknownGeneration(t *testing.T) {
	old := syntheticSchema47(t)
	for _, state := range []string{"active", "disabled"} {
		lifecycleSQL(t, old, `INSERT INTO tenants(id,display_name,status,created_by,created_at,updated_at) VALUES(?,?,?,'bootstrap','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, state, state, state)
	}
	old.Close()
	db, err := Open(old.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	policy := config.AccessPolicy{Tenants: map[string]model.Tenant{}}
	for _, state := range []string{"active", "disabled"} {
		policy.Tenants[state] = model.Tenant{ID: state, DisplayName: state, Status: state}
	}
	policy.Tenants["new-active"] = model.Tenant{ID: "new-active", DisplayName: "New", Status: "active"}
	for i := 0; i < 2; i++ {
		for _, tenant := range policy.Tenants {
			if err := db.ReconcileBootstrapAccess(context.Background(), []model.Tenant{tenant}, nil, nil); err != nil {
				t.Fatal(err)
			}
		}
		if i == 0 {
			if _, err := db.SaveAccessPolicySnapshot(context.Background(), lifecycleSnapshot(t, policy), 0); err != nil {
				t.Fatal(err)
			}
		}
		for id, want := range map[string]int64{"active": 0, "disabled": 0, "new-active": 1} {
			value, err := db.GetTenantLifecycle(context.Background(), id)
			if err != nil || value.Generation != want {
				t.Fatalf("%s: %+v %v", id, value, err)
			}
		}
	}
}
