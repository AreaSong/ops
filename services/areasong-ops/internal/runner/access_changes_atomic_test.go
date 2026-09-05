package runner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

type atomicAccessFixture struct {
	engine         *Engine
	database       *store.Store
	creator        string
	firstApprover  string
	secondApprover string
	executor       string
}

func newAtomicAccessFixture(t *testing.T) atomicAccessFixture {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Now().UTC()
	fixture := atomicAccessFixture{
		database:       database,
		creator:        config.AccessHashForEmail("atomic-creator@example.test"),
		firstApprover:  config.AccessHashForEmail("atomic-first@example.test"),
		secondApprover: config.AccessHashForEmail("atomic-second@example.test"),
		executor:       config.AccessHashForEmail("atomic-executor@example.test"),
	}
	admin := model.Role{
		ID: "platform-admin", DisplayName: "Platform admin", BuiltIn: true,
		Permissions: []model.Permission{model.Permission("*"), model.PermissionManageAccess},
	}
	catalog := &config.Catalog{
		SchemaVersion: 4,
		Access: &config.AccessPolicy{
			Enforced: true, DefaultTenant: "default",
			Tenants: map[string]model.Tenant{
				"default": {ID: "default", DisplayName: "Default", Status: "active", CreatedAt: now},
			},
			Roles: map[string]model.Role{"platform-admin": admin},
			Principals: map[string]config.AccessPrincipal{
				fixture.creator:        {Subject: fixture.creator, TenantID: "default", Roles: []string{"platform-admin"}},
				fixture.firstApprover:  {Subject: fixture.firstApprover, TenantID: "default", Roles: []string{"platform-admin"}},
				fixture.secondApprover: {Subject: fixture.secondApprover, TenantID: "default", Roles: []string{"platform-admin"}},
				fixture.executor:       {Subject: fixture.executor, TenantID: "default", Roles: []string{"platform-admin"}},
			},
		},
	}
	fixture.engine, err = NewEngineChecked(catalog, database, &fakeExecutor{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture atomicAccessFixture) approvedExecutorRevocation(t *testing.T, key string) model.AccessChange {
	t.Helper()
	ctx := context.Background()
	change, created, err := fixture.engine.CreateAccessChange(ctx, fixture.creator, model.AccessControlUpdateRequest{
		RequiresDualApproval:    true,
		RemovePrincipalSubjects: []string{fixture.executor},
		IdempotencyKey:          key,
	})
	if err != nil || !created {
		t.Fatalf("create change=%+v created=%v err=%v", change, created, err)
	}
	approval := model.AccessChangeApprovalRequest{
		Digest: change.RequestDigest, Confirmation: change.ConfirmationPhrase,
	}
	if _, err := fixture.engine.ApproveAccessChange(ctx, fixture.firstApprover, change.ID, approval); err != nil {
		t.Fatal(err)
	}
	change, err = fixture.engine.ApproveAccessChange(ctx, fixture.secondApprover, change.ID, approval)
	if err != nil || change.State != model.AccessChangeApproved {
		t.Fatalf("approve change=%+v err=%v", change, err)
	}
	return change
}

func TestAccessChangeRevokingExecutorIsAtomicAndRetryable(t *testing.T) {
	fixture := newAtomicAccessFixture(t)
	ctx := context.Background()
	change := fixture.approvedExecutorRevocation(t, "77777777-7777-4777-8777-777777777777")
	before, found, err := fixture.database.GetAccessPolicySnapshot(ctx)
	if err != nil || !found {
		t.Fatalf("initial snapshot found=%v err=%v", found, err)
	}

	applied, err := fixture.engine.ApplyAccessChange(ctx, fixture.executor, change.ID)
	if err != nil || applied.State != model.AccessChangeApplied {
		t.Fatalf("apply=%+v err=%v", applied, err)
	}
	if applied.AppliedByHash != fixture.executor || applied.AppliedPolicyDigest == "" ||
		applied.AppliedPolicyVersion != before.Version+1 || applied.AppliedAt == nil {
		t.Fatalf("applied envelope is incomplete: %+v", applied)
	}
	if err := fixture.engine.authorizePlatform(ctx, fixture.executor, model.PermissionManageAccess, "access"); err == nil {
		t.Fatal("executor retained access.manage after revocation")
	}

	adminView, err := fixture.engine.AccessControl(ctx, fixture.firstApprover)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.engine.UpdateAccess(ctx, fixture.firstApprover, model.AccessControlUpdateRequest{
		Tenants:         []model.Tenant{{ID: "post-change", DisplayName: "Post change", Status: "active"}},
		ExpectedVersion: adminView.Version,
		IdempotencyKey:  "99999999-9999-4999-8999-999999999999",
	}); err != nil {
		t.Fatalf("unrelated admin update failed: %v", err)
	}
	postChange, _, err := fixture.database.GetAccessPolicySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if postChange.Version != applied.AppliedPolicyVersion+1 {
		t.Fatalf("unrelated update version=%d applied=%d", postChange.Version, applied.AppliedPolicyVersion)
	}
	retried, err := fixture.engine.ApplyAccessChange(ctx, fixture.executor, change.ID)
	if err != nil || retried.AppliedPolicyDigest != applied.AppliedPolicyDigest ||
		retried.AppliedPolicyVersion != applied.AppliedPolicyVersion {
		t.Fatalf("retry=%+v err=%v", retried, err)
	}
	intruder := config.AccessHashForEmail("atomic-intruder@example.test")
	if _, err := fixture.engine.ApplyAccessChange(ctx, intruder, change.ID); !errors.Is(err, store.ErrActorMismatch) {
		t.Fatalf("intruder retry err=%v want ErrActorMismatch", err)
	}
	for _, actor := range []string{"", "not-a-valid-actor"} {
		if _, err := fixture.engine.ApplyAccessChange(ctx, actor, change.ID); err == nil {
			t.Fatalf("invalid actor %q retried an applied change", actor)
		}
	}
	after, found, err := fixture.database.GetAccessPolicySnapshot(ctx)
	if err != nil || !found || after.Version != postChange.Version || after.Digest != postChange.Digest {
		t.Fatalf("snapshot before=%+v after=%+v found=%v err=%v", before, after, found, err)
	}
	assertAtomicAccessAuditCounts(t, fixture.database, change.ID, 2, 1)

	changes, err := fixture.database.ListAccessChanges(ctx, 10)
	if err != nil || len(changes) != 1 || changes[0].AppliedByHash != fixture.executor ||
		changes[0].AppliedPolicyDigest != applied.AppliedPolicyDigest ||
		changes[0].AppliedPolicyVersion != applied.AppliedPolicyVersion {
		t.Fatalf("listed changes=%+v err=%v", changes, err)
	}
}

func TestUpdateAccessCannotImpersonateApprovedExecution(t *testing.T) {
	fixture := newAtomicAccessFixture(t)
	ctx := context.Background()
	change := fixture.approvedExecutorRevocation(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	intruder := config.AccessHashForEmail("unregistered-intruder@example.test")

	if _, err := fixture.engine.UpdateAccess(ctx, intruder, model.AccessControlUpdateRequest{
		RemovePrincipalSubjects: []string{fixture.executor},
		IdempotencyKey:          change.IdempotencyKey,
	}); err == nil {
		t.Fatal("unregistered caller bypassed access.manage through UpdateAccess")
	}
	stored, err := fixture.database.GetAccessChange(ctx, change.ID)
	if err != nil || stored.State != model.AccessChangeApproved || stored.AppliedAt != nil {
		t.Fatalf("approved envelope changed after rejected direct call: %+v err=%v", stored, err)
	}
	if err := fixture.engine.authorizePlatform(ctx, fixture.executor, model.PermissionManageAccess, "access"); err != nil {
		t.Fatalf("executor permission changed after rejected direct call: %v", err)
	}
}

func TestConcurrentAccessChangeExecutionCommitsOnce(t *testing.T) {
	fixture := newAtomicAccessFixture(t)
	ctx := context.Background()
	change := fixture.approvedExecutorRevocation(t, "88888888-8888-4888-8888-888888888888")
	before, _, err := fixture.database.GetAccessPolicySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			applied, applyErr := fixture.engine.ApplyAccessChange(ctx, fixture.executor, change.ID)
			if applyErr == nil && applied.State != model.AccessChangeApplied {
				applyErr = errors.New("并发执行未返回 applied 终态")
			}
			results <- applyErr
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	after, _, err := fixture.database.GetAccessPolicySnapshot(ctx)
	if err != nil || after.Version != before.Version+1 {
		t.Fatalf("snapshot version before=%d after=%d err=%v", before.Version, after.Version, err)
	}
	assertAtomicAccessAuditCounts(t, fixture.database, change.ID, 1, 1)
}

func assertAtomicAccessAuditCounts(t *testing.T, database *store.Store, changeID string, policyUpdates, applies int) {
	t.Helper()
	entries, err := database.ListAudit(context.Background(), 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	var policyCount, applyCount int
	for _, entry := range entries {
		switch {
		case entry.Event == "access.policy.updated":
			policyCount++
		case entry.Event == "access.change.applied" && entry.Resource == "access/"+changeID:
			applyCount++
		}
	}
	if policyCount != policyUpdates || applyCount != applies {
		t.Fatalf("audit counts policy=%d apply=%d want policy=%d apply=%d", policyCount, applyCount, policyUpdates, applies)
	}
}
