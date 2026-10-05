package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestTenantLifecycleReviewEntityIDs(t *testing.T) {
	for _, kind := range []string{"tenant", "role", "initial-seed"} {
		t.Run(kind, func(t *testing.T) {
			db, policy := lifecycleFixture(t)
			ctx := context.Background()
			version := int64(1)
			if kind == "role" {
				policy.Roles["kind"] = model.Role{ID: "kind", DisplayName: "Kind", Permissions: []model.Permission{model.PermissionRead}}
			} else {
				tenant := model.Tenant{ID: "kind", DisplayName: "Kind", Status: "active", CreatedBy: lifecycleActor}
				policy.Tenants["kind"] = tenant
				if err := db.UpsertTenant(ctx, tenant); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "initial-seed" {
				lifecycleSQL(t, db, `DELETE FROM access_policy_snapshots WHERE version=1`)
				version = 0
			}
			if _, err := db.SaveAccessPolicySnapshot(ctx, lifecycleSnapshot(t, policy), version); err != nil {
				t.Fatal("legal entity ID rejected", err)
			}
			other := policy.Tenants["other"]
			other.DisplayName = "Renamed"
			policy.Tenants["other"] = other
			snapshot := lifecycleSnapshot(t, policy)
			if _, _, err := db.ApplyAccessPolicyMutation(ctx, AccessPolicyMutation{Actor: lifecycleActor, IdempotencyKey: "kind-rename", RequestDigest: snapshot.Digest, Snapshot: snapshot, ExpectedVersion: version + 1, Tenants: []model.Tenant{other}}); err != nil {
				t.Fatal("unrelated update rejected", err)
			}
		})
	}
}

func TestTenantLifecycleReviewFirstSnapshotReferences(t *testing.T) {
	for _, kind := range []string{"principal-tenant", "principal-role", "binding-tenant", "binding-role"} {
		t.Run(kind, func(t *testing.T) {
			db, policy := lifecycleFixture(t)
			ctx := context.Background()
			lifecycleSQL(t, db, `DELETE FROM access_policy_snapshots WHERE version=1`)
			principal := policy.Principals[lifecycleApprover]
			switch kind {
			case "principal-tenant":
				principal.TenantID = "missing"
			case "principal-role":
				principal.Roles = []string{"missing"}
			case "binding-tenant":
				policy.Bindings[0].TenantID = "missing"
			case "binding-role":
				policy.Bindings[0].RoleID = "missing"
			}
			policy.Principals[lifecycleApprover] = principal
			snapshot := lifecycleSnapshot(t, policy)
			before := lifecycleContents(t, db)
			if _, err := db.SaveAccessPolicySnapshot(ctx, snapshot, 0); err == nil {
				t.Error("first snapshot allowed dangling reference")
			}
			assertLifecycleUnchanged(t, db, before)
			if _, _, err := db.ApplyAccessPolicyMutation(ctx, AccessPolicyMutation{Actor: lifecycleActor, IdempotencyKey: "first-dangling", RequestDigest: snapshot.Digest, ExpectedVersion: 0, Snapshot: snapshot}); err == nil {
				t.Error("first mutation allowed dangling reference")
			}
			assertLifecycleUnchanged(t, db, before)
		})
	}
}

func TestTenantLifecycleReviewDanglingReferences(t *testing.T) {
	for _, target := range []string{"tenant", "role"} {
		t.Run(target, func(t *testing.T) {
			db, _ := lifecycleFixture(t)
			ctx := context.Background()
			execution := approveLifecycle(t, db, lifecycleRequest(1, 1, "disabled"))
			if _, err := db.applyTenantLifecycleChange(ctx, execution); err != nil {
				t.Fatal(err)
			}
			current, _, err := db.GetAccessPolicySnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var policy config.AccessPolicy
			if err := json.Unmarshal([]byte(current.PolicyJSON), &policy); err != nil {
				t.Fatal(err)
			}
			if target == "tenant" {
				delete(policy.Tenants, "team")
			} else {
				delete(policy.Roles, "viewer")
			}
			snapshot := lifecycleSnapshot(t, policy)
			before := lifecycleContents(t, db)
			if _, err := db.SaveAccessPolicySnapshot(ctx, snapshot, 2); err == nil {
				t.Error("snapshot allowed dangling reference")
			}
			assertLifecycleUnchanged(t, db, before)
			if _, _, err := db.ApplyAccessPolicyMutation(ctx, AccessPolicyMutation{Actor: lifecycleActor, IdempotencyKey: "dangling", RequestDigest: snapshot.Digest, ExpectedVersion: 2, Snapshot: snapshot}); err == nil {
				t.Error("mutation allowed dangling reference")
			}
			assertLifecycleUnchanged(t, db, before)
		})
	}
}

func TestTenantLifecycleReviewAdminEligibility(t *testing.T) {
	for _, kind := range []string{"disabled", "expired", "jit", "valid-jit", "legacy-admin-binding"} {
		t.Run(kind, func(t *testing.T) {
			db, policy := lifecycleFixture(t)
			principal := policy.Principals[lifecycleActor]
			expired := time.Now().UTC().Add(-time.Hour)
			switch kind {
			case "disabled", "legacy-admin-binding":
				principal.Status = "disabled"
			case "expired":
				principal.ExpiresAt = &expired
			case "jit", "valid-jit":
				principal.JIT = true
			}
			if kind == "valid-jit" || kind == "legacy-admin-binding" {
				role := "viewer"
				if kind == "legacy-admin-binding" {
					role = "platform-admin"
				}
				policy.Bindings = append(policy.Bindings, model.RoleBinding{ID: "admin-eligibility", Subject: lifecycleActor, TenantID: "default", RoleID: role, JIT: true})
			}
			policy.Principals[lifecycleActor] = principal
			snapshot := lifecycleSnapshot(t, policy)
			lifecycleSQL(t, db, `UPDATE access_policy_snapshots SET policy_json=?,digest=? WHERE version=1`, snapshot.PolicyJSON, snapshot.Digest)
			execution := approveLifecycle(t, db, lifecycleRequest(1, 1, "disabled"))
			before := lifecycleContents(t, db)
			_, err := db.applyTenantLifecycleChange(context.Background(), execution)
			if kind == "valid-jit" || kind == "legacy-admin-binding" {
				if err != nil {
					t.Fatal("existing admin rule narrowed", err)
				}
				return
			}
			if err == nil {
				t.Error("unusable principal counted as last administrator")
			}
			assertLifecycleUnchanged(t, db, before)
		})
	}
}

func TestTenantLifecycleAtomicFailures(t *testing.T) {
	triggers := map[string]string{
		"cas":             "BEFORE UPDATE ON tenants WHEN NEW.lifecycle_generation!=OLD.lifecycle_generation",
		"snapshot":        "BEFORE INSERT ON access_policy_snapshots",
		"receipt":         "BEFORE INSERT ON access_mutation_receipts",
		"policy-audit":    "BEFORE INSERT ON audit_entries WHEN NEW.event='access.policy.updated'",
		"lifecycle-audit": "BEFORE INSERT ON audit_entries WHEN NEW.event='tenant.lifecycle.changed'",
		"applied":         "BEFORE UPDATE ON access_changes WHEN NEW.state='applied'",
		"closure-audit":   "BEFORE INSERT ON audit_entries WHEN NEW.event='access.change.applied'",
	}
	for name, trigger := range triggers {
		t.Run(name, func(t *testing.T) {
			db, _ := lifecycleFixture(t)
			execution := approveLifecycle(t, db, lifecycleRequest(1, 1, "disabled"))
			lifecycleSQL(t, db, `CREATE TRIGGER lifecycle_failure `+trigger+` BEGIN SELECT RAISE(ABORT,'synthetic lifecycle failure'); END`)
			before := lifecycleContents(t, db)
			if _, err := db.applyTenantLifecycleChange(context.Background(), execution); err == nil || !strings.Contains(err.Error(), "synthetic lifecycle failure") {
				t.Fatal(err)
			}
			assertLifecycleUnchanged(t, db, before)
			path := db.path
			db.Close()
			reopened, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			assertLifecycleUnchanged(t, reopened, before)
			lifecycleSQL(t, reopened, `DROP TRIGGER lifecycle_failure`)
			if _, err := reopened.applyTenantLifecycleChange(context.Background(), execution); err != nil {
				t.Fatal(err)
			}
			after := lifecycleContents(t, reopened)
			if _, err := reopened.applyTenantLifecycleChange(context.Background(), execution); err != nil {
				t.Fatal(err)
			}
			assertLifecycleUnchanged(t, reopened, after)
			var receipts int
			if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM access_mutation_receipts WHERE idempotency_key=?`, execution.IdempotencyKey).Scan(&receipts); err != nil || receipts != 1 {
				t.Fatalf("receipts=%d err=%v", receipts, err)
			}
		})
	}
}

func TestTenantLifecycleTwoConnectionRace(t *testing.T) {
	for _, same := range []bool{false, true} {
		name := "different-request"
		if same {
			name = "same-request"
		}
		t.Run(name, func(t *testing.T) {
			first, _ := lifecycleFixture(t)
			request := lifecycleRequest(1, 1, "disabled")
			one := approveLifecycle(t, first, request)
			two := one
			if !same {
				request.IdempotencyKey = "competing-request"
				two = approveLifecycle(t, first, request)
			}
			second, err := Open(first.path)
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			arrived := make(chan struct{}, 1)
			release := make(chan struct{})
			var once sync.Once
			first.now = func() time.Time { once.Do(func() { arrived <- struct{}{}; <-release }); return time.Now().UTC() }
			lifecycleSQL(t, second, `PRAGMA busy_timeout=0`)
			results := make(chan error, 2)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			go func() { _, err := first.applyTenantLifecycleChange(ctx, one); results <- err }()
			select {
			case <-arrived:
			case <-ctx.Done():
				close(release)
				t.Fatal(ctx.Err())
			}
			// 首个转换已持写锁，第二连接确定性拒绝，不等两个事务同时进入锁内。
			_, blocked := second.applyTenantLifecycleChange(ctx, two)
			close(release)
			if blocked == nil || !strings.Contains(blocked.Error(), "SQLITE_BUSY") {
				t.Fatalf("writer not excluded: %v", blocked)
			}
			results <- blocked
			successes := 0
			for i := 0; i < 2; i++ {
				err := <-results
				if err == nil {
					successes++
					continue
				}
				if !errors.Is(err, ErrAccessVersion) && !errors.Is(err, ErrTenantLifecycleConflict) && !strings.Contains(err.Error(), "SQLITE_BUSY") {
					t.Fatal(err)
				}
			}
			if successes != 1 {
				t.Fatalf("commits=%d", successes)
			}
			value, err := first.GetTenantLifecycle(ctx, "team")
			if err != nil || value.Generation != 2 || value.PolicyVersion != 2 {
				t.Fatalf("%+v %v", value, err)
			}
			var applied, approved, receipts int
			if err := first.db.QueryRow(`SELECT COUNT(*) FROM access_changes WHERE state='applied'`).Scan(&applied); err != nil {
				t.Fatal(err)
			}
			if err := first.db.QueryRow(`SELECT COUNT(*) FROM access_changes WHERE state='approved'`).Scan(&approved); err != nil {
				t.Fatal(err)
			}
			if err := first.db.QueryRow(`SELECT COUNT(*) FROM access_mutation_receipts`).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if applied != 1 || receipts != 1 || (!same && approved != 1) || (same && approved != 0) {
				t.Fatalf("applied=%d approved=%d receipts=%d", applied, approved, receipts)
			}
			for _, event := range []string{"access.policy.updated", "tenant.lifecycle.changed", "access.change.applied"} {
				var count int
				if err := first.db.QueryRow(`SELECT COUNT(*) FROM audit_entries WHERE event=?`, event).Scan(&count); err != nil || count != 1 {
					t.Fatalf("%s=%d %v", event, count, err)
				}
			}
			if same {
				before := lifecycleContents(t, first)
				change, err := second.applyTenantLifecycleChange(ctx, one)
				if err != nil || change.State != model.AccessChangeApplied {
					t.Fatal(err)
				}
				assertLifecycleUnchanged(t, first, before)
			}
		})
	}
}

func TestTenantLifecycleProtectedAndInconsistentTargets(t *testing.T) {
	for _, kind := range []string{"configured-default", "bootstrap", "source", "state", "alias", "snapshot-only", "row-only", "binding-row-only", "version-overflow"} {
		t.Run(kind, func(t *testing.T) {
			db, policy := lifecycleFixture(t)
			tenant := policy.Tenants["team"]
			request := lifecycleRequest(1, 1, "disabled")
			switch kind {
			case "configured-default":
				policy.DefaultTenant = "team"
			case "bootstrap":
				tenant.CreatedBy = "bootstrap"
				lifecycleSQL(t, db, `UPDATE tenants SET created_by='bootstrap' WHERE id='team'`)
			case "source":
				tenant.CreatedBy = ""
			case "state":
				tenant.Status = "other"
				lifecycleSQL(t, db, `UPDATE tenants SET status='other' WHERE id='team'`)
			case "alias":
				lifecycleSQL(t, db, `INSERT INTO tenants(id,display_name,status,created_by,created_at,updated_at) VALUES('TEAM','Alias','active',?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, lifecycleActor)
			case "snapshot-only":
				lifecycleSQL(t, db, `DELETE FROM tenants WHERE id='team'`)
			case "binding-row-only":
				if err := db.UpsertRoleBinding(context.Background(), model.RoleBinding{ID: "row-only", Subject: lifecycleActor, TenantID: "team", RoleID: "viewer", CreatedBy: lifecycleActor}); err != nil {
					t.Fatal(err)
				}
			case "version-overflow":
				request.ExpectedVersion = int64(^uint64(0) >> 1)
			}
			policy.Tenants["team"] = tenant
			if kind == "row-only" {
				delete(policy.Tenants, "team")
			}
			snapshot := lifecycleSnapshot(t, policy)
			lifecycleSQL(t, db, `UPDATE access_policy_snapshots SET policy_json=?,digest=?,version=? WHERE version=1`, snapshot.PolicyJSON, snapshot.Digest, request.ExpectedVersion)
			execution := approveLifecycle(t, db, request)
			before := lifecycleContents(t, db)
			_, err := db.applyTenantLifecycleChange(context.Background(), execution)
			if err == nil {
				t.Fatal("invalid target accepted")
			}
			if (kind == "configured-default" || kind == "bootstrap") && !errors.Is(err, ErrTenantLifecycleProtected) {
				t.Fatal(err)
			}
			assertLifecycleUnchanged(t, db, before)
		})
	}
}
