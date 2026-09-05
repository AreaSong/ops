package store

import (
	"context"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestFleetRegistrationStateAndAuditAreAtomic(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	server := model.ServerNode{
		ID: "server-a", Hostname: "server-a", Environment: "production",
		RunnerID: "runner-a", State: model.NodeUnknown,
	}
	serverAudit := model.AuditEntry{
		ActorHash: "actor-a", Event: "fleet.server_registered", Resource: server.ID,
		Outcome: "accepted", Detail: map[string]any{"hostname": server.Hostname},
	}

	installFleetAuditFailure(t, database)
	if err := database.RegisterServerNode(ctx, server, serverAudit); err == nil {
		t.Fatal("server registration survived audit failure")
	}
	assertFleetCounts(t, database, 0, 0, 0)
	dropFleetAuditFailure(t, database)
	if err := database.RegisterServerNode(ctx, server, serverAudit); err != nil {
		t.Fatal(err)
	}
	assertFleetCounts(t, database, 1, 0, 1)

	runner := model.RunnerNode{
		ID: "runner-a", ServerID: server.ID, Hostname: "runner-a",
		Version: "v1", State: model.NodeUnknown,
	}
	runnerAudit := model.AuditEntry{
		ActorHash: "actor-a", Event: "fleet.runner_registered", Resource: runner.ID,
		Outcome: "accepted", Detail: map[string]any{"tenantId": "tenant-a"},
	}
	installFleetAuditFailure(t, database)
	if err := database.RegisterRunnerNode(ctx, runner, "tenant-a", runnerAudit); err == nil {
		t.Fatal("runner registration survived audit failure")
	}
	assertFleetCounts(t, database, 1, 0, 1)
	dropFleetAuditFailure(t, database)
	if err := database.RegisterRunnerNode(ctx, runner, "tenant-a", runnerAudit); err != nil {
		t.Fatal(err)
	}
	assertFleetCounts(t, database, 1, 1, 2)
}

func TestFleetRegistrationUpdatesRollBackWhenAuditFails(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	server := model.ServerNode{
		ID: "server-a", Hostname: "server-a", Environment: "production",
		RunnerID: "runner-a", State: model.NodeUnknown,
	}
	if err := database.UpsertServerNode(ctx, server); err != nil {
		t.Fatal(err)
	}
	runner := model.RunnerNode{
		ID: "runner-a", ServerID: server.ID, Hostname: "runner-a",
		Version: "v1", State: model.NodeUnknown,
	}
	if err := database.UpsertRunnerNode(ctx, runner, "tenant-a"); err != nil {
		t.Fatal(err)
	}
	installFleetAuditFailure(t, database)

	server.Hostname = "changed-server"
	if err := database.RegisterServerNode(ctx, server, model.AuditEntry{
		ActorHash: "actor-a", Event: "fleet.server_registered", Resource: server.ID, Outcome: "accepted",
	}); err == nil {
		t.Fatal("server update survived audit failure")
	}
	runner.Version = "v2"
	if err := database.RegisterRunnerNode(ctx, runner, "tenant-a", model.AuditEntry{
		ActorHash: "actor-a", Event: "fleet.runner_registered", Resource: runner.ID, Outcome: "accepted",
	}); err == nil {
		t.Fatal("runner update survived audit failure")
	}

	fleet, err := database.ListFleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(fleet.Servers) != 1 || fleet.Servers[0].Hostname != "server-a" {
		t.Fatalf("failed audited server update changed state: %+v", fleet.Servers)
	}
	if len(fleet.Runners) != 1 || fleet.Runners[0].Version != "v1" {
		t.Fatalf("failed audited runner update changed state: %+v", fleet.Runners)
	}
	assertFleetCounts(t, database, 1, 1, 0)
}

func installFleetAuditFailure(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`CREATE TRIGGER fail_fleet_audit BEFORE INSERT ON audit_entries
		WHEN NEW.event LIKE 'fleet.%registered' BEGIN SELECT RAISE(ABORT, 'forced fleet audit failure'); END`); err != nil {
		t.Fatal(err)
	}
}

func dropFleetAuditFailure(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`DROP TRIGGER fail_fleet_audit`); err != nil {
		t.Fatal(err)
	}
}

func assertFleetCounts(t *testing.T, database *Store, servers, runners, audits int) {
	t.Helper()
	fleet, err := database.ListFleet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fleet.Servers) != servers || len(fleet.Runners) != runners {
		t.Fatalf("fleet counts servers=%d runners=%d, want %d/%d", len(fleet.Servers), len(fleet.Runners), servers, runners)
	}
	entries, err := database.ListAudit(context.Background(), 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != audits {
		t.Fatalf("audit count=%d, want %d", len(entries), audits)
	}
}
