package store

import (
	"context"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestKubernetesOperationIdempotencyAndFailureState(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	target := model.KubernetesTarget{
		Cluster: "cluster", Context: "context", Namespace: "namespace",
		Allowlist: []string{"deployment/demo"}, ResourceKinds: []string{"Deployment"},
	}
	operation := model.KubernetesOperation{
		ID: "kube-op-1", IdempotencyKey: "kube-key-1", Target: target,
		Action: "apply", ManifestDigest: "sha256:manifest", State: "pending", Phase: "preflight",
		RolloutState: "pending", RolloutResources: []string{"deployment/demo"},
		CreatedAt: time.Now().UTC(),
	}
	created, output, fresh, err := database.BeginKubernetesOperation(ctx, operation, "actor-a", "sha256:req")
	if err != nil || !fresh || output != "" || created.ID != operation.ID {
		t.Fatalf("created=%+v output=%q fresh=%v err=%v", created, output, fresh, err)
	}
	replayed, output, fresh, err := database.BeginKubernetesOperation(ctx, model.KubernetesOperation{
		ID: "different-id", IdempotencyKey: operation.IdempotencyKey, Target: target,
		Action: "apply", ManifestDigest: operation.ManifestDigest, State: "pending",
		CreatedAt: time.Now().UTC(),
	}, "actor-a", "sha256:req")
	if err != nil || fresh || replayed.ID != operation.ID || output != "" {
		t.Fatalf("replayed=%+v output=%q fresh=%v err=%v", replayed, output, fresh, err)
	}
	if _, _, _, err := database.BeginKubernetesOperation(ctx, model.KubernetesOperation{
		ID: "different-id-2", IdempotencyKey: operation.IdempotencyKey, Target: target,
		Action: "apply", ManifestDigest: "sha256:other", State: "pending",
		CreatedAt: time.Now().UTC(),
	}, "actor-a", "sha256:other"); err != ErrIdempotency {
		t.Fatalf("conflicting idempotency error=%v", err)
	}
	if err := database.UpdateKubernetesOperation(ctx, operation.ID, "needs_attention", "kubectl output", "apply failed"); err != nil {
		t.Fatal(err)
	}
	stored, output, err := database.GetKubernetesOperation(ctx, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != "needs_attention" || stored.Error != "apply failed" || output != "kubectl output" {
		t.Fatalf("stored=%+v output=%q", stored, output)
	}
	if len(stored.Target.Allowlist) != 1 || stored.Target.Allowlist[0] != "deployment/demo" {
		t.Fatalf("target allowlist was not persisted: %+v", stored.Target)
	}
	if stored.Phase != "preflight" || stored.RolloutState != "pending" ||
		len(stored.RolloutResources) != 1 || stored.RolloutResources[0] != "deployment/demo" {
		t.Fatalf("rollout state was not persisted: %+v", stored)
	}
}

func TestKubernetesPlanDualApprovalAndIdempotentReplay(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	target := model.KubernetesTarget{
		Cluster: "cluster", Context: "context", Namespace: "namespace", TenantID: "tenant-a",
		Allowlist: []string{"deployment/demo"}, ResourceKinds: []string{"Deployment"},
	}
	plan := model.KubernetesPlan{
		ID: "kube-plan-1", IdempotencyKey: "kube-plan-key-1", RequestDigest: "sha256:req",
		ActorHash: "actor-a", TenantID: "tenant-a", Target: target, ManifestDigest: "sha256:manifest",
		Action: "apply", State: "pending_approval", ConfirmationPhrase: "批准 Kubernetes 变更 cluster/namespace sha256:manifest",
		RequiresDualApproval: true, CreatedAt: now,
	}
	manifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: demo\n"
	created, fresh, err := database.CreateKubernetesPlan(ctx, plan, manifest, HashConfirmation(plan.ConfirmationPhrase))
	if err != nil || !fresh || created.ID != plan.ID {
		t.Fatalf("created=%+v fresh=%v err=%v", created, fresh, err)
	}
	replayed, fresh, err := database.CreateKubernetesPlan(ctx, plan, manifest, HashConfirmation(plan.ConfirmationPhrase))
	if err != nil || fresh || replayed.ID != plan.ID {
		t.Fatalf("replayed=%+v fresh=%v err=%v", replayed, fresh, err)
	}
	if _, err := database.ApproveKubernetesPlan(ctx, plan.ID, plan.ActorHash, plan.ManifestDigest, plan.ConfirmationPhrase); err == nil {
		t.Fatal("plan creator unexpectedly approved own Kubernetes plan")
	}
	if _, err := database.ApproveKubernetesPlan(ctx, plan.ID, "actor-b", plan.ManifestDigest, "wrong confirmation"); err != ErrConfirmation {
		t.Fatalf("wrong confirmation error=%v", err)
	}
	first, err := database.ApproveKubernetesPlan(ctx, plan.ID, "actor-b", plan.ManifestDigest, plan.ConfirmationPhrase)
	if err != nil || first.ApprovedByHash != "actor-b" || first.State != "pending_approval" {
		t.Fatalf("first approval=%+v err=%v", first, err)
	}
	if _, err := database.ApproveKubernetesPlan(ctx, plan.ID, "actor-b", plan.ManifestDigest, plan.ConfirmationPhrase); err != ErrActorMismatch {
		t.Fatalf("same approver error=%v", err)
	}
	second, err := database.ApproveKubernetesPlan(ctx, plan.ID, "actor-c", plan.ManifestDigest, plan.ConfirmationPhrase)
	if err != nil || second.State != "approved" || second.SecondApprovedByHash != "actor-c" {
		t.Fatalf("second approval=%+v err=%v", second, err)
	}
	operation := model.KubernetesOperation{
		ID: "plan-kube-plan-1", IdempotencyKey: "plan-kube-plan-1", RequestDigest: "sha256:operation",
		Target: target, TenantID: plan.TenantID, Action: plan.Action, ManifestDigest: plan.ManifestDigest,
		State: "pending", Phase: "preflight", RolloutState: "pending",
		RolloutResources: []string{"deployment/demo"},
	}
	started, ok, err := database.StartKubernetesPlan(ctx, plan.ID, "actor-d", "exec-key", operation)
	if err != nil || !ok || started.State != "running" {
		t.Fatalf("start=%+v ok=%v err=%v", started, ok, err)
	}
	if started.ExecuteIdempotencyKey != "exec-key" {
		t.Fatalf("execute idempotency key=%q", started.ExecuteIdempotencyKey)
	}
	if _, _, err := database.StartKubernetesPlan(ctx, plan.ID, "actor-d", "other-exec-key", operation); err != ErrIdempotency {
		t.Fatalf("execute replay conflict error=%v", err)
	}
	if _, _, err := database.StartKubernetesPlan(ctx, plan.ID, "actor-e", "exec-key", operation); err != ErrActorMismatch {
		t.Fatalf("execute actor replay error=%v", err)
	}
	conflictingOperation := operation
	conflictingOperation.ID, conflictingOperation.IdempotencyKey = "other", "other"
	if _, _, err := database.StartKubernetesPlan(ctx, plan.ID, "actor-d", "exec-key", conflictingOperation); err != ErrIdempotency {
		t.Fatalf("replay conflict error=%v", err)
	}
	if err := database.FinishKubernetesPlan(ctx, plan.ID, "needs_attention", "apply failed"); err != nil {
		t.Fatal(err)
	}
	stored, manifestOut, err := database.GetKubernetesPlanWithManifest(ctx, plan.ID)
	if err != nil || stored.State != "needs_attention" || manifestOut != manifest {
		t.Fatalf("stored=%+v manifest=%q err=%v", stored, manifestOut, err)
	}
}
