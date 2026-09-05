package store

import (
	"context"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestKubernetesPlanStateAndAuditAreAtomic(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Round(0)
	database.now = func() time.Time { return now }
	plan, manifest := atomicKubernetesPlan(now)

	installKubernetesAuditFailure(t, database)
	if _, _, err := database.CreateKubernetesPlan(
		ctx, plan, manifest, HashConfirmation(plan.ConfirmationPhrase),
	); err == nil {
		t.Fatal("Kubernetes plan creation survived audit failure")
	}
	assertKubernetesPlanState(t, database, plan.ID, ErrNotFound, "")
	dropKubernetesAuditFailure(t, database)
	if _, created, err := database.CreateKubernetesPlan(
		ctx, plan, manifest, HashConfirmation(plan.ConfirmationPhrase),
	); err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}

	installKubernetesAuditFailure(t, database)
	if _, err := database.ApproveKubernetesPlan(
		ctx, plan.ID, "actor-b", plan.ManifestDigest, plan.ConfirmationPhrase,
	); err == nil {
		t.Fatal("Kubernetes first approval survived audit failure")
	}
	assertKubernetesPlanState(t, database, plan.ID, nil, "pending_approval")
	dropKubernetesAuditFailure(t, database)
	if _, err := database.ApproveKubernetesPlan(
		ctx, plan.ID, "actor-b", plan.ManifestDigest, plan.ConfirmationPhrase,
	); err != nil {
		t.Fatal(err)
	}

	installKubernetesAuditFailure(t, database)
	if _, err := database.ApproveKubernetesPlan(
		ctx, plan.ID, "actor-c", plan.ManifestDigest, plan.ConfirmationPhrase,
	); err == nil {
		t.Fatal("Kubernetes second approval survived audit failure")
	}
	assertKubernetesPlanState(t, database, plan.ID, nil, "pending_approval")
	dropKubernetesAuditFailure(t, database)
	if _, err := database.ApproveKubernetesPlan(
		ctx, plan.ID, "actor-c", plan.ManifestDigest, plan.ConfirmationPhrase,
	); err != nil {
		t.Fatal(err)
	}

	operation := atomicKubernetesOperation(plan)
	installKubernetesAuditFailure(t, database)
	if _, _, err := database.StartKubernetesPlan(
		ctx, plan.ID, "actor-d", "execute-key", operation,
	); err == nil {
		t.Fatal("Kubernetes start survived audit failure")
	}
	assertKubernetesPlanState(t, database, plan.ID, nil, "approved")
	if _, _, err := database.GetKubernetesOperation(ctx, operation.ID); err != ErrNotFound {
		t.Fatalf("operation survived start rollback: %v", err)
	}
	dropKubernetesAuditFailure(t, database)
	if _, started, err := database.StartKubernetesPlan(
		ctx, plan.ID, "actor-d", "execute-key", operation,
	); err != nil || !started {
		t.Fatalf("started=%v err=%v", started, err)
	}
	if err := database.UpdateKubernetesOperationRollout(
		ctx, operation.ID, "succeeded", "health", "succeeded",
		operation.RolloutResources, "ok", "",
	); err != nil {
		t.Fatal(err)
	}

	installKubernetesAuditFailure(t, database)
	if err := database.FinishKubernetesPlan(ctx, plan.ID, "succeeded", ""); err == nil {
		t.Fatal("Kubernetes finish survived audit failure")
	}
	assertKubernetesPlanState(t, database, plan.ID, nil, "running")
	dropKubernetesAuditFailure(t, database)
	if err := database.FinishKubernetesPlan(ctx, plan.ID, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	assertKubernetesPlanState(t, database, plan.ID, nil, "succeeded")
	assertKubernetesAuditEvents(t, database, plan.ID)
}

func atomicKubernetesPlan(now time.Time) (model.KubernetesPlan, string) {
	target := model.KubernetesTarget{
		Cluster: "cluster-a", Context: "context-a", Namespace: "namespace-a",
		TenantID: "tenant-a", Allowlist: []string{"deployment/demo"},
		ResourceKinds: []string{"Deployment"},
	}
	return model.KubernetesPlan{
		ID:             "11111111-1111-4111-8111-111111111111",
		IdempotencyKey: "22222222-2222-4222-8222-222222222222",
		RequestDigest:  "sha256:request", ActorHash: "actor-a", TenantID: "tenant-a",
		Target: target, ManifestDigest: "sha256:manifest", Action: "apply",
		State: "pending_approval", ConfirmationPhrase: "批准 Kubernetes 变更",
		RequiresDualApproval: true, CreatedAt: now,
	}, "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: demo\n"
}

func atomicKubernetesOperation(plan model.KubernetesPlan) model.KubernetesOperation {
	return model.KubernetesOperation{
		ID: "plan-" + plan.ID, IdempotencyKey: "plan-" + plan.ID,
		RequestDigest: "sha256:operation", TenantID: plan.TenantID, Target: plan.Target,
		Action: plan.Action, ManifestDigest: plan.ManifestDigest, State: "pending",
		Phase: "preflight", RolloutState: "pending",
		RolloutResources: []string{"deployment/demo"},
	}
}

func assertKubernetesPlanState(t *testing.T, database *Store, id string, wantErr error, state string) {
	t.Helper()
	plan, err := database.GetKubernetesPlan(context.Background(), id)
	if err != wantErr {
		t.Fatalf("plan error=%v want=%v", err, wantErr)
	}
	if err == nil && plan.State != state {
		t.Fatalf("plan state=%q want=%q", plan.State, state)
	}
}

func assertKubernetesAuditEvents(t *testing.T, database *Store, planID string) {
	t.Helper()
	entries, err := database.ListAudit(context.Background(), 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"kubernetes.plan.created": 1, "kubernetes.plan.approved": 2,
		"kubernetes.plan.started": 1, "kubernetes.plan.executed": 1,
	}
	for _, entry := range entries {
		if entry.Resource == planID {
			want[entry.Event]--
		}
	}
	for event, remaining := range want {
		if remaining != 0 {
			t.Fatalf("audit event %s remaining=%d entries=%+v", event, remaining, entries)
		}
	}
}

func installKubernetesAuditFailure(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`CREATE TRIGGER fail_kubernetes_audit BEFORE INSERT ON audit_entries
		WHEN NEW.event LIKE 'kubernetes.%'
		BEGIN SELECT RAISE(ABORT, 'forced Kubernetes audit failure'); END`); err != nil {
		t.Fatal(err)
	}
}

func dropKubernetesAuditFailure(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`DROP TRIGGER fail_kubernetes_audit`); err != nil {
		t.Fatal(err)
	}
}
