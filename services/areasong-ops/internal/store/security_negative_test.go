package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestHeartbeatReceiptRejectsNonceReplayIncludingPayloadChange(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	database.now = func() time.Time { return now }
	for _, id := range []string{"runner-a", "runner-b"} {
		serverID := "server-" + id[len(id)-1:]
		if err := database.UpsertServerNode(ctx, model.ServerNode{
			ID: serverID, Hostname: serverID, Environment: "production",
			RunnerID: id, State: model.NodeUnknown,
		}); err != nil {
			t.Fatal(err)
		}
		if err := database.UpsertRunnerNode(ctx, model.RunnerNode{
			ID: id, ServerID: serverID, Hostname: id,
			Version: "v1", State: model.NodeOnline,
		}, "tenant-a"); err != nil {
			t.Fatal(err)
		}
	}
	first, err := database.HeartbeatRunnerWithReceipt(ctx, "runner-a", "v2", time.Minute, "nonce-1", "sha256:first")
	if err != nil {
		t.Fatal(err)
	}
	if first.LastHeartbeatNonce != "nonce-1" || first.Version != "v2" {
		t.Fatalf("first heartbeat=%+v", first)
	}
	fleet, err := database.ListFleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(fleet.Servers) != 2 || fleet.Servers[0].State != model.NodeOnline || fleet.Servers[0].LastHeartbeat == nil {
		t.Fatalf("signed heartbeat did not atomically refresh server: %+v", fleet.Servers)
	}
	// The exact storage error is intentionally opaque; callers only need a
	// deterministic rejection and no partially updated node.
	if _, err := database.HeartbeatRunnerWithReceipt(ctx, "runner-a", "v3", time.Minute, "nonce-1", "sha256:changed"); err == nil {
		t.Fatal("nonce replay was accepted")
	}
	stored, found, err := database.GetRunnerNode(ctx, "runner-a")
	if err != nil || !found {
		t.Fatalf("stored runner=%+v found=%v err=%v", stored, found, err)
	}
	if stored.Version != "v2" {
		t.Fatalf("replayed heartbeat changed version to %q", stored.Version)
	}
	// Nonces are scoped to a Runner identity; reusing the token on a different
	// registered Runner is not a replay of runner-a's message.
	if _, err := database.HeartbeatRunnerWithReceipt(ctx, "runner-b", "v2", time.Minute, "nonce-1", "sha256:other"); err != nil {
		t.Fatalf("same nonce on another runner rejected: %v", err)
	}
}

func TestHeartbeatRollsBackRunnerAndReceiptWhenServerBindingIsMissing(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	database.now = func() time.Time { return now }
	if err := database.UpsertRunnerNode(ctx, model.RunnerNode{
		ID: "runner-orphan", ServerID: "server-orphan", Hostname: "runner-orphan",
		Version: "v1", State: model.NodeUnknown,
	}, "tenant-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.HeartbeatRunnerWithReceipt(ctx, "runner-orphan", "v2", time.Minute, "nonce-orphan", "sha256:orphan"); err == nil {
		t.Fatal("orphan Runner heartbeat was accepted")
	}
	stored, found, err := database.GetRunnerNode(ctx, "runner-orphan")
	if err != nil || !found {
		t.Fatalf("stored runner=%+v found=%v err=%v", stored, found, err)
	}
	if stored.Version != "v1" || stored.State != model.NodeUnknown || stored.LastHeartbeat != nil || stored.LeaseGeneration != 0 {
		t.Fatalf("failed heartbeat partially changed Runner: %+v", stored)
	}
	if err := database.UpsertServerNode(ctx, model.ServerNode{
		ID: "server-orphan", Hostname: "server-orphan", Environment: "production",
		RunnerID: "runner-orphan", State: model.NodeUnknown,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.HeartbeatRunnerWithReceipt(ctx, "runner-orphan", "v2", time.Minute, "nonce-orphan", "sha256:orphan"); err != nil {
		t.Fatalf("rolled-back nonce was unexpectedly retained: %v", err)
	}
}

func TestKubernetesOperationsAreTenantFilteredAtStorageBoundary(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	created := time.Now().UTC()
	for _, operation := range []model.KubernetesOperation{
		{ID: "tenant-op-a", IdempotencyKey: "tenant-key-a", TenantID: "tenant-a", Target: model.KubernetesTarget{Cluster: "cluster-a", Context: "ctx-a", Namespace: "ns-a", TenantID: "tenant-a", Allowlist: []string{"deployment/a"}, ResourceKinds: []string{"Deployment"}}, Action: "validate", State: "succeeded", CreatedAt: created},
		{ID: "tenant-op-b", IdempotencyKey: "tenant-key-b", TenantID: "tenant-b", Target: model.KubernetesTarget{Cluster: "cluster-b", Context: "ctx-b", Namespace: "ns-b", TenantID: "tenant-b", Allowlist: []string{"deployment/b"}, ResourceKinds: []string{"Deployment"}}, Action: "validate", State: "succeeded", CreatedAt: created.Add(time.Second)},
	} {
		if err := database.SaveKubernetesOperation(ctx, operation, "actor", "output"); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name   string
		tenant string
		id     string
	}{
		{name: "tenant-a", tenant: "tenant-a", id: "tenant-op-a"},
		{name: "tenant-b", tenant: "tenant-b", id: "tenant-op-b"},
	} {
		t.Run(test.name, func(t *testing.T) {
			operations, err := database.ListKubernetesOperationsForTenant(ctx, test.tenant, 50)
			if err != nil {
				t.Fatal(err)
			}
			if len(operations) != 1 || operations[0].ID != test.id || operations[0].TenantID != test.tenant {
				t.Fatalf("tenant %s saw %+v", test.tenant, operations)
			}
		})
	}
}

func TestStoreAuthorizationDoesNotCrossTenantBinding(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if err := database.UpsertTenant(ctx, model.Tenant{ID: "tenant-a", DisplayName: "Tenant A", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertTenant(ctx, model.Tenant{ID: "tenant-b", DisplayName: "Tenant B", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertRole(ctx, model.Role{ID: "tenant-reader", DisplayName: "Tenant reader", Permissions: []model.Permission{model.PermissionRead}}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertRoleBinding(ctx, model.RoleBinding{
		ID: "binding-a", Subject: "actor-a", TenantID: "tenant-a", RoleID: "tenant-reader", ObjectIDs: []string{"service:a"}, CreatedBy: "test",
	}); err != nil {
		t.Fatal(err)
	}
	allowed, err := database.Authorize(ctx, "actor-a", "tenant-a", "service:a", model.PermissionRead)
	if err != nil || !allowed.Allowed {
		t.Fatalf("same-tenant authorization=%+v err=%v", allowed, err)
	}
	crossTenant, err := database.Authorize(ctx, "actor-a", "tenant-b", "service:a", model.PermissionRead)
	if err != nil {
		t.Fatal(err)
	}
	if crossTenant.Allowed {
		t.Fatalf("cross-tenant binding was accepted: %+v", crossTenant)
	}
}

func TestMarkExtensionStoredRejectsFailedReservation(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	result := model.ExtensionUploadResult{
		Manifest:       model.ExtensionManifest{ID: "demo-ext", Version: "v1", Digest: "sha256:digest", Publisher: "publisher"},
		IdempotencyKey: "extension-key", StorageDigest: "sha256:digest", State: "staging", CreatedAt: time.Now().UTC(),
	}
	if _, created, err := database.ReserveExtensionPackage(ctx, result, "actor", "sha256:req", "/tmp/extension.package"); err != nil || !created {
		t.Fatalf("reserve created=%v err=%v", created, err)
	}
	if err := database.MarkExtensionFailed(ctx, result.Manifest.ID, result.Manifest.Version); err != nil {
		t.Fatal(err)
	}
	if err := database.MarkExtensionStored(ctx, result.Manifest.ID, result.Manifest.Version); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stored failed reservation err=%v", err)
	}
}
