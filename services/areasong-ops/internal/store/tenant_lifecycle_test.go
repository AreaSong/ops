package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

var lifecycleActor = strings.Repeat("a", 64)
var lifecycleApprover = strings.Repeat("b", 64)

func lifecycleFixture(t *testing.T) (*Store, config.AccessPolicy) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "synthetic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := db.EnsureAccessDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	policy := config.AccessPolicy{Enforced: true, DefaultTenant: "default",
		Tenants: map[string]model.Tenant{
			"default": {ID: "default", DisplayName: "Default", Status: "active", CreatedBy: "bootstrap"},
			"team":    {ID: "team", DisplayName: "Team", Status: "active", CreatedBy: lifecycleActor},
			"other":   {ID: "other", DisplayName: "Other", Status: "active", CreatedBy: lifecycleActor},
		},
		Roles: map[string]model.Role{
			"platform-admin": {ID: "platform-admin", DisplayName: "Admin", Permissions: []model.Permission{"*"}},
			"viewer":         {ID: "viewer", DisplayName: "Viewer", Permissions: []model.Permission{model.PermissionRead}},
		},
		Principals: map[string]config.AccessPrincipal{
			lifecycleActor:    {Subject: lifecycleActor, TenantID: "default", Roles: []string{"platform-admin"}, Status: "active"},
			lifecycleApprover: {Subject: lifecycleApprover, TenantID: "team", Roles: []string{"viewer"}, Status: "active"},
		},
		Bindings: []model.RoleBinding{{ID: "held", Subject: lifecycleApprover, TenantID: "team", RoleID: "viewer", ObjectIDs: []string{"service:synthetic"}, CreatedBy: lifecycleActor}},
	}
	for _, tenant := range policy.Tenants {
		if err := db.UpsertTenant(ctx, tenant); err != nil {
			t.Fatal(err)
		}
	}
	for _, binding := range policy.Bindings {
		if err := db.UpsertRoleBinding(ctx, binding); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.SaveAccessPolicySnapshot(ctx, lifecycleSnapshot(t, policy), 0); err != nil {
		t.Fatal(err)
	}
	seedLifecycleHistory(t, db)
	return db, policy
}

func seedLifecycleHistory(t *testing.T, db *Store) {
	t.Helper()
	for _, state := range []string{"approved", "completed"} {
		lifecycleSQL(t, db, `INSERT INTO release_plans(id,actor_hash,service,action,target,risk,state,digest,approval_summary_json,confirmation_hash,created_at,updated_at,tenant_id)
			VALUES(?,?,'synthetic','inspect','','low',?,'historical-digest','{"tenantId":"team"}','confirmation','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','team')`, "historical-"+state, lifecycleActor, state)
	}
}

func lifecycleSnapshot(t *testing.T, policy config.AccessPolicy) model.AccessPolicySnapshot {
	t.Helper()
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	return model.AccessPolicySnapshot{PolicyJSON: string(raw), Digest: digestPolicyJSON(string(raw)), ActorHash: lifecycleActor}
}

func lifecycleRequest(version, generation int64, target string) model.TenantLifecycleTransitionRequest {
	from := "active"
	if target == "active" {
		from = "disabled"
	}
	return model.TenantLifecycleTransitionRequest{Kind: model.TenantLifecycleKind, TenantID: "team", ExpectedStatus: from, ExpectedGeneration: generation,
		TargetStatus: target, ExpectedVersion: version, RequiresDualApproval: true, IdempotencyKey: fmt.Sprintf("lifecycle-%d-%d-%s", version, generation, target)}
}

func approveLifecycle(t *testing.T, db *Store, request model.TenantLifecycleTransitionRequest) tenantLifecycleExecution {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return approveLifecyclePayload(t, db, string(raw), request.IdempotencyKey)
}

func approveLifecyclePayload(t *testing.T, db *Store, raw, key string) tenantLifecycleExecution {
	t.Helper()
	ctx := context.Background()
	change := model.AccessChange{ID: key, IdempotencyKey: key, ActorHash: lifecycleActor, RequestDigest: digestPolicyJSON(raw),
		RequiresDualApproval: true, ApprovalPolicy: model.ApprovalPolicyTwoParty, ConfirmationPhrase: "synthetic lifecycle"}
	if _, _, err := db.CreateAccessChange(ctx, change, raw, HashConfirmation(change.ConfirmationPhrase)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ApproveAccessChange(ctx, change.ID, lifecycleApprover, change.RequestDigest, change.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	return tenantLifecycleExecution{ChangeID: change.ID, Actor: lifecycleActor, RequestDigest: change.RequestDigest, IdempotencyKey: key}
}

// 比较所有真实表内容和 user_version，而不是只比较计数。
func lifecycleContents(t *testing.T, db *Store) map[string]string {
	t.Helper()
	rows, err := db.db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, name := range tables {
		rows, err := db.db.Query(`SELECT * FROM "` + strings.ReplaceAll(name, `"`, `""`) + `"`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var contents []string
		for rows.Next() {
			values := make([]any, len(columns))
			ptrs := make([]any, len(columns))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			contents = append(contents, string(raw))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		sort.Strings(contents)
		encoded, _ := json.Marshal(contents)
		result[name] = string(encoded)
	}
	var version int
	if err := db.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	result["user_version"] = fmt.Sprint(version)
	return result
}

func assertLifecycleUnchanged(t *testing.T, db *Store, before map[string]string) {
	t.Helper()
	after := lifecycleContents(t, db)
	if !reflect.DeepEqual(before, after) {
		for table, value := range before {
			if after[table] != value {
				t.Errorf("table changed: %s\nbefore=%s\nafter=%s", table, value, after[table])
			}
		}
	}
}

func TestTenantLifecycleRoundTripAndReplay(t *testing.T) {
	db, policy := lifecycleFixture(t)
	ctx := context.Background()
	initial, _, _ := db.GetAccessPolicySnapshot(ctx)
	before := lifecycleContents(t, db)
	down := approveLifecycle(t, db, lifecycleRequest(1, 1, "disabled"))
	applied, err := db.applyTenantLifecycleChange(ctx, down)
	if err != nil || applied.AppliedPolicyVersion != 2 {
		t.Fatalf("%+v %v", applied, err)
	}
	value, err := db.GetTenantLifecycle(ctx, "team")
	if err != nil || value.Status != "disabled" || value.Generation != 2 || value.PolicyVersion != 2 {
		t.Fatalf("%+v %v", value, err)
	}
	after := lifecycleContents(t, db)
	for _, table := range []string{"role_bindings", "roles", "release_plans", "tasks", "recovery_points"} {
		if before[table] != after[table] {
			t.Fatal("unrelated rows changed", table)
		}
	}
	snapshot, _, _ := db.GetAccessPolicySnapshot(ctx)
	var disabled config.AccessPolicy
	if err := json.Unmarshal([]byte(snapshot.PolicyJSON), &disabled); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(policy.Principals, disabled.Principals) || !reflect.DeepEqual(policy.Bindings, disabled.Bindings) {
		t.Fatal("references changed")
	}
	old := policy.Tenants["team"]
	changed := disabled.Tenants["team"]
	if old.ID != changed.ID || old.DisplayName != changed.DisplayName || old.CreatedAt != changed.CreatedAt || old.CreatedBy != changed.CreatedBy {
		t.Fatal("creation metadata changed")
	}
	// 另一租户改名不得因已停用租户的原引用而失败，也不得推进代次。
	other := disabled.Tenants["other"]
	other.DisplayName = "Renamed"
	disabled.Tenants["other"] = other
	mutation := AccessPolicyMutation{Actor: lifecycleActor, IdempotencyKey: "ordinary-rename", RequestDigest: lifecycleSnapshot(t, disabled).Digest,
		ExpectedVersion: 2, Snapshot: lifecycleSnapshot(t, disabled), Tenants: []model.Tenant{other}}
	if _, _, err := db.ApplyAccessPolicyMutation(ctx, mutation); err != nil {
		t.Fatal(err)
	}
	up := approveLifecycle(t, db, lifecycleRequest(3, 2, "active"))
	if _, err := db.applyTenantLifecycleChange(ctx, up); err != nil {
		t.Fatal(err)
	}
	value, err = db.GetTenantLifecycle(ctx, "team")
	if err != nil || value.Status != "active" || value.Generation != 3 || value.PolicyVersion != 4 {
		t.Fatalf("%+v %v", value, err)
	}
	final := lifecycleContents(t, db)
	if replay, err := db.applyTenantLifecycleChange(ctx, down); err != nil || replay.AppliedPolicyVersion != 2 {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	assertLifecycleUnchanged(t, db, final)
	historical, found, err := db.GetAccessPolicySnapshotVersion(ctx, 1)
	if err != nil || !found || historical.PolicyJSON != initial.PolicyJSON || historical.Digest != initial.Digest {
		t.Fatal("historical snapshot changed")
	}
	for event, want := range map[string]int{"tenant.lifecycle.changed": 2, "access.policy.updated": 2, "access.change.applied": 2} {
		var count int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM audit_entries WHERE event=?`, event).Scan(&count); err != nil || count != want {
			t.Fatalf("%s=%d err=%v", event, count, err)
		}
	}
	path := db.path
	db.Close()
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertLifecycleUnchanged(t, reopened, final)
}

func TestTenantLifecycleRejectsWithoutWrites(t *testing.T) {
	cases := map[string]func(*testing.T, *Store, *model.TenantLifecycleTransitionRequest, *tenantLifecycleExecution){
		"actor": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			e.Actor = strings.Repeat("c", 64)
		},
		"digest": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			e.RequestDigest = "wrong"
		},
		"key": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			e.IdempotencyKey = "wrong"
		},
		"version": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			r.ExpectedVersion = 2
		},
		"generation": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			r.ExpectedGeneration = 2
		},
		"zero-generation": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			r.ExpectedGeneration = 0
		},
		"zero-version": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			r.ExpectedVersion = 0
		},
		"same-state": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			r.TargetStatus = "active"
		},
		"wrong-state": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			r.ExpectedStatus = "disabled"
			r.TargetStatus = "active"
		},
		"missing": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			r.TenantID = "missing"
		},
		"default": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			r.TenantID = "default"
		},
		"uppercase-id": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			r.TenantID = "TEAM"
		},
		"kind": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			r.Kind = "other"
		},
		"dual": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			r.RequiresDualApproval = false
		},
		"unknown": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			lifecycleSQL(t, d, `UPDATE tenants SET lifecycle_generation=0 WHERE id='team'`)
		},
		"overflow": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			lifecycleSQL(t, d, `UPDATE tenants SET lifecycle_generation=? WHERE id='team'`, int64(math.MaxInt64))
			r.ExpectedGeneration = math.MaxInt64
		},
		"mirror": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			lifecycleSQL(t, d, `UPDATE tenants SET display_name='drift' WHERE id='team'`)
		},
		"source": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			lifecycleSQL(t, d, `UPDATE tenants SET created_by='' WHERE id='team'`)
		},
		"binding-drift": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			lifecycleSQL(t, d, `UPDATE role_bindings SET role_id='platform-admin' WHERE id='held'`)
		},
		"binding-missing": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			lifecycleSQL(t, d, `DELETE FROM role_bindings WHERE id='held'`)
		},
		"binding-source": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			lifecycleSQL(t, d, `UPDATE role_bindings SET created_by='' WHERE id='held'`)
		},
		"unapproved": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			lifecycleSQL(t, d, `UPDATE access_changes SET state='pending_approval' WHERE id=?`, e.ChangeID)
		},
		"self-approved": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			lifecycleSQL(t, d, `UPDATE access_changes SET approved_by_hash=actor_hash WHERE id=?`, e.ChangeID)
		},
		"approval-policy": func(t *testing.T, d *Store, r *model.TenantLifecycleTransitionRequest, e *tenantLifecycleExecution) {
			lifecycleSQL(t, d, `UPDATE access_changes SET approval_policy='' WHERE id=?`, e.ChangeID)
		},
	}
	for name, alter := range cases {
		t.Run(name, func(t *testing.T) {
			db, _ := lifecycleFixture(t)
			request := lifecycleRequest(1, 1, "disabled")
			execution := approveLifecycle(t, db, request)
			alter(t, db, &request, &execution)
			if name != "digest" {
				raw, _ := json.Marshal(request)
				lifecycleSQL(t, db, `UPDATE access_changes SET payload_json=?,request_digest=? WHERE id=?`, string(raw), digestPolicyJSON(string(raw)), execution.ChangeID)
				execution.RequestDigest = digestPolicyJSON(string(raw))
			}
			before := lifecycleContents(t, db)
			if _, err := db.applyTenantLifecycleChange(context.Background(), execution); err == nil {
				t.Fatal("accepted invalid transition")
			}
			assertLifecycleUnchanged(t, db, before)
		})
	}
}

func lifecycleSQL(t *testing.T, db *Store, query string, args ...any) {
	t.Helper()
	if _, err := db.db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestTenantLifecycleOrdinaryGuards(t *testing.T) {
	db, policy := lifecycleFixture(t)
	ctx := context.Background()
	down := approveLifecycle(t, db, lifecycleRequest(1, 1, "disabled"))
	if _, err := db.applyTenantLifecycleChange(ctx, down); err != nil {
		t.Fatal(err)
	}
	before := lifecycleContents(t, db)
	if err := db.UpsertTenant(ctx, policy.Tenants["team"]); !errors.Is(err, ErrTenantLifecycleConflict) {
		t.Fatal(err)
	}
	if _, err := db.SaveAccessPolicySnapshot(ctx, lifecycleSnapshot(t, policy), 2); err == nil {
		t.Fatal("snapshot re-enabled tenant")
	}
	if _, _, err := db.ApplyAccessPolicyMutation(ctx, AccessPolicyMutation{Actor: lifecycleActor, IdempotencyKey: "bypass", RequestDigest: lifecycleSnapshot(t, policy).Digest, ExpectedVersion: 2, Snapshot: lifecycleSnapshot(t, policy)}); err == nil {
		t.Fatal("mutation re-enabled tenant")
	}
	for _, status := range []string{"disabled", "other", "ACTIVE", " disabled "} {
		if err := db.UpsertTenant(ctx, model.Tenant{ID: "new", DisplayName: "New", Status: status, CreatedBy: lifecycleActor}); err == nil {
			t.Fatal("created non-active tenant", status)
		}
	}
	assertLifecycleUnchanged(t, db, before)
	// 普通公开审批方法也不能把新载荷闭合为空成功。
	up := approveLifecycle(t, db, lifecycleRequest(2, 2, "active"))
	before = lifecycleContents(t, db)
	_, err := db.ApplyAccessChangeMutation(ctx, up.ChangeID, up.Actor, AccessPolicyMutation{Actor: up.Actor, IdempotencyKey: up.IdempotencyKey, AccessChangeDigest: up.RequestDigest, ExpectedVersion: 2, Snapshot: lifecycleSnapshot(t, policy), RequestDigest: lifecycleSnapshot(t, policy).Digest})
	if !errors.Is(err, model.ErrTenantLifecyclePayload) {
		t.Fatal(err)
	}
	assertLifecycleUnchanged(t, db, before)
}

func TestTenantLifecycleEmptyStatusCAS(t *testing.T) {
	db, policy := lifecycleFixture(t)
	tenant := policy.Tenants["team"]
	tenant.Status = ""
	policy.Tenants["team"] = tenant
	raw := lifecycleSnapshot(t, policy)
	lifecycleSQL(t, db, `UPDATE tenants SET status='' WHERE id='team'`)
	lifecycleSQL(t, db, `UPDATE access_policy_snapshots SET policy_json=?,digest=? WHERE version=1`, raw.PolicyJSON, raw.Digest)
	execution := approveLifecycle(t, db, lifecycleRequest(1, 1, "disabled"))
	if _, err := db.applyTenantLifecycleChange(context.Background(), execution); err != nil {
		t.Fatal(err)
	}
}

func TestTenantLifecycleReplayDifferentActor(t *testing.T) {
	db, _ := lifecycleFixture(t)
	execution := approveLifecycle(t, db, lifecycleRequest(1, 1, "disabled"))
	if _, err := db.applyTenantLifecycleChange(context.Background(), execution); err != nil {
		t.Fatal(err)
	}
	before := lifecycleContents(t, db)
	execution.Actor = lifecycleApprover
	if _, err := db.applyTenantLifecycleChange(context.Background(), execution); !errors.Is(err, ErrActorMismatch) {
		t.Fatal(err)
	}
	assertLifecycleUnchanged(t, db, before)
}
