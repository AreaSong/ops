package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const atomicBaselinePolicy = `{"enforced":true,"roles":{"platform-admin":{"permissions":["*"]}},"principals":{"admin":{"roles":["platform-admin"]}},"bindings":[]}`

func TestApplyAccessChangeMutationRollsBackEveryRecordOnFailure(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if err := database.EnsureAccessDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertRole(ctx, model.Role{
		ID: "held-role", DisplayName: "Held role", Permissions: []model.Permission{model.PermissionRead}, CreatedBy: "seed-actor",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertRoleBinding(ctx, model.RoleBinding{
		ID: "held-binding", Subject: "subject", TenantID: "default", RoleID: "held-role",
		CreatedBy: "seed-actor", ApprovalState: "applied",
	}); err != nil {
		t.Fatal(err)
	}
	baseline, err := database.SaveAccessPolicySnapshot(ctx, model.AccessPolicySnapshot{
		Digest: digestPolicyJSON(atomicBaselinePolicy), PolicyJSON: atomicBaselinePolicy, ActorHash: "bootstrap",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	change := approvedStoreAccessChange(t, database, "rollback-change", "rollback-key", "sha256:rollback-change")
	proposedPolicy := `{"enforced":true,"roles":{"platform-admin":{"permissions":["*"]}},"principals":{"admin":{"roles":["platform-admin"]}},"tenants":{"transient":{"id":"transient"}},"bindings":[]}`
	mutation := AccessPolicyMutation{
		Actor: "executor", IdempotencyKey: change.IdempotencyKey,
		RequestDigest: digestPolicyJSON(proposedPolicy), AccessChangeDigest: change.RequestDigest,
		ExpectedVersion: baseline.Version,
		Snapshot: model.AccessPolicySnapshot{
			Digest: digestPolicyJSON(proposedPolicy), PolicyJSON: proposedPolicy, ActorHash: "executor",
		},
		Tenants:       []model.Tenant{{ID: "transient", DisplayName: "Transient", Status: "active", CreatedBy: "executor"}},
		RemoveRoleIDs: []string{"held-role"},
		Audit: &model.AuditEntry{
			ActorHash: "executor", Event: "access.policy.updated", Resource: "access", Outcome: "accepted",
		},
	}
	if _, err := database.ApplyAccessChangeMutation(ctx, change.ID, "executor", mutation); err == nil {
		t.Fatal("mutation unexpectedly succeeded while deleting a referenced role")
	}

	tenants, err := database.ListTenants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tenant := range tenants {
		if tenant.ID == "transient" {
			t.Fatal("tenant row survived a failed access-change transaction")
		}
	}
	roles, err := database.ListRoles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !containsRole(roles, "held-role") {
		t.Fatal("referenced role changed during rollback")
	}
	bindings, err := database.ListRoleBindings(ctx)
	if err != nil || !containsBinding(bindings, "held-binding") {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	after, found, err := database.GetAccessPolicySnapshot(ctx)
	if err != nil || !found || after.Version != baseline.Version || after.Digest != baseline.Digest {
		t.Fatalf("snapshot baseline=%+v after=%+v found=%v err=%v", baseline, after, found, err)
	}
	stored, err := database.GetAccessChange(ctx, change.ID)
	if err != nil || stored.State != model.AccessChangeApproved || stored.AppliedByHash != "" || stored.AppliedAt != nil {
		t.Fatalf("access change=%+v err=%v", stored, err)
	}
	var receipts int
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM access_mutation_receipts WHERE idempotency_key=?`, change.IdempotencyKey).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("receipt count=%d err=%v", receipts, err)
	}
	entries, err := database.ListAudit(ctx, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || countAuditEvent(entries, "access.policy.updated") != 0 ||
		countAuditEvent(entries, "access.change.applied") != 0 {
		t.Fatalf("failed mutation left unexpected audit entries=%+v", entries)
	}
}

func TestApplyAccessChangeMutationRollsBackWhenClosureAuditFails(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	baseline, err := database.SaveAccessPolicySnapshot(ctx, model.AccessPolicySnapshot{
		Digest: digestPolicyJSON(atomicBaselinePolicy), PolicyJSON: atomicBaselinePolicy, ActorHash: "bootstrap",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	change := approvedStoreAccessChange(
		t, database, "closure-audit-change", "closure-audit-key", "sha256:closure-audit-change",
	)
	proposedPolicy := "{\"enforced\":true,\"roles\":{\"platform-admin\":{\"permissions\":[\"*\"]}},\"principals\":{\"admin\":{\"roles\":[\"platform-admin\"]}},\"bindings\":[],\"revision\":\"closure-audit\"}"
	mutation := AccessPolicyMutation{
		Actor: "executor", IdempotencyKey: change.IdempotencyKey,
		RequestDigest: digestPolicyJSON(proposedPolicy), AccessChangeDigest: change.RequestDigest,
		ExpectedVersion: baseline.Version,
		Snapshot: model.AccessPolicySnapshot{
			Digest: digestPolicyJSON(proposedPolicy), PolicyJSON: proposedPolicy, ActorHash: "executor",
		},
		Audit: &model.AuditEntry{
			ActorHash: "executor", Event: "access.policy.updated", Resource: "access", Outcome: "accepted",
		},
	}
	if _, err := database.db.Exec("CREATE TRIGGER access_change_closure_audit_failure " +
		"BEFORE INSERT ON audit_entries WHEN NEW.event='access.change.applied' " +
		"BEGIN SELECT RAISE(ABORT, 'closure audit failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ApplyAccessChangeMutation(ctx, change.ID, "executor", mutation); err == nil {
		t.Fatal("mutation unexpectedly committed while closure audit failed")
	}
	after, found, err := database.GetAccessPolicySnapshot(ctx)
	if err != nil || !found || after.Version != baseline.Version || after.Digest != baseline.Digest {
		t.Fatalf("snapshot baseline=%+v after=%+v found=%v err=%v", baseline, after, found, err)
	}
	stored, err := database.GetAccessChange(ctx, change.ID)
	if err != nil || stored.State != model.AccessChangeApproved || stored.AppliedByHash != "" ||
		stored.AppliedPolicyDigest != "" || stored.AppliedPolicyVersion != 0 || stored.AppliedAt != nil {
		t.Fatalf("failed closure left access change=%+v err=%v", stored, err)
	}
	var receipts int
	if err := database.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM access_mutation_receipts WHERE idempotency_key=?",
		change.IdempotencyKey,
	).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("receipt count=%d err=%v", receipts, err)
	}
	assertStoreAtomicAuditCounts(t, database, change.ID, 0, 0)
}

func TestApplyAccessChangeMutationRecoversExistingReceiptIdempotently(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	baseline, err := database.SaveAccessPolicySnapshot(ctx, model.AccessPolicySnapshot{
		Digest: digestPolicyJSON(atomicBaselinePolicy), PolicyJSON: atomicBaselinePolicy, ActorHash: "bootstrap",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	change := approvedStoreAccessChange(t, database, "recovery-change", "recovery-key", "sha256:recovery-change")
	proposedPolicy := `{"enforced":true,"defaultTenant":"default","roles":{"platform-admin":{"permissions":["*"]}},"principals":{"remaining-admin":{"roles":["platform-admin"]}},"bindings":[]}`
	mutation := AccessPolicyMutation{
		Actor: "executor", IdempotencyKey: change.IdempotencyKey,
		RequestDigest: digestPolicyJSON(proposedPolicy), AccessChangeDigest: change.RequestDigest,
		ExpectedVersion: baseline.Version,
		Snapshot: model.AccessPolicySnapshot{
			Digest: digestPolicyJSON(proposedPolicy), PolicyJSON: proposedPolicy, ActorHash: "executor",
		},
		Audit: &model.AuditEntry{
			ActorHash: "executor", Event: "access.policy.updated", Resource: "access", Outcome: "accepted",
		},
	}
	// Simulate a historical interruption after the policy/receipt transaction
	// committed but before older code marked the approval envelope applied. The
	// public normal-write entry now rejects approval context, so this package-
	// internal fixture deliberately constructs the legacy residue in one tx.
	tx, err := database.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	appliedSnapshot, created, err := database.applyAccessPolicyMutationTx(ctx, tx, mutation)
	if err != nil || !created {
		_ = tx.Rollback()
		t.Fatalf("seed interrupted mutation snapshot=%+v created=%v err=%v", appliedSnapshot, created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	stillApproved, err := database.GetAccessChange(ctx, change.ID)
	if err != nil || stillApproved.State != model.AccessChangeApproved {
		t.Fatalf("pre-recovery change=%+v err=%v", stillApproved, err)
	}

	applied, err := database.ApplyAccessChangeMutation(ctx, change.ID, "executor", mutation)
	if err != nil || applied.State != model.AccessChangeApplied || applied.AppliedByHash != "executor" ||
		applied.AppliedPolicyDigest != appliedSnapshot.Digest || applied.AppliedPolicyVersion != appliedSnapshot.Version {
		t.Fatalf("recovered change=%+v snapshot=%+v err=%v", applied, appliedSnapshot, err)
	}
	retried, err := database.ApplyAccessChangeMutation(ctx, change.ID, "executor", mutation)
	if err != nil || retried.AppliedPolicyDigest != applied.AppliedPolicyDigest {
		t.Fatalf("retry=%+v err=%v", retried, err)
	}
	otherMutation := mutation
	otherMutation.Actor = "other-executor"
	if _, err := database.ApplyAccessChangeMutation(ctx, change.ID, "other-executor", otherMutation); !errors.Is(err, ErrActorMismatch) {
		t.Fatalf("different executor err=%v want ErrActorMismatch", err)
	}
	latest, _, err := database.GetAccessPolicySnapshot(ctx)
	if err != nil || latest.Version != baseline.Version+1 {
		t.Fatalf("latest snapshot=%+v err=%v", latest, err)
	}
	assertStoreAtomicAuditCounts(t, database, change.ID, 1, 1)
}

func approvedStoreAccessChange(t *testing.T, database *Store, id, key, digest string) model.AccessChange {
	t.Helper()
	ctx := context.Background()
	change := model.AccessChange{
		ID: id, IdempotencyKey: key, RequestDigest: digest, ActorHash: "creator",
		State: model.AccessChangePendingApproval, RequiresDualApproval: true,
		ConfirmationPhrase: "批准访问策略变更 " + digest, CreatedAt: time.Now().UTC(),
	}
	stored, created, err := database.CreateAccessChange(ctx, change, `{}`, HashConfirmation(change.ConfirmationPhrase))
	if err != nil || !created {
		t.Fatalf("create change=%+v created=%v err=%v", stored, created, err)
	}
	if _, err := database.ApproveAccessChange(ctx, id, "first-approver", digest, change.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	stored, err = database.ApproveAccessChange(ctx, id, "second-approver", digest, change.ConfirmationPhrase)
	if err != nil || stored.State != model.AccessChangeApproved {
		t.Fatalf("approve change=%+v err=%v", stored, err)
	}
	return stored
}

func containsRole(roles []model.Role, id string) bool {
	for _, role := range roles {
		if role.ID == id {
			return true
		}
	}
	return false
}

func containsBinding(bindings []model.RoleBinding, id string) bool {
	for _, binding := range bindings {
		if binding.ID == id {
			return true
		}
	}
	return false
}

func assertStoreAtomicAuditCounts(t *testing.T, database *Store, changeID string, policyUpdates, applies int) {
	t.Helper()
	entries, err := database.ListAudit(context.Background(), 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	var policyCount, applyCount int
	for _, entry := range entries {
		if entry.Event == "access.policy.updated" {
			policyCount++
		}
		if entry.Event == "access.change.applied" && entry.Resource == "access/"+changeID {
			applyCount++
		}
	}
	if policyCount != policyUpdates || applyCount != applies {
		t.Fatalf("audit counts policy=%d apply=%d want policy=%d apply=%d", policyCount, applyCount, policyUpdates, applies)
	}
}
