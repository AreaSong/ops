package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const kubernetesPreviewManifest = "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: app-a\n  namespace: ns-a\nspec:\n  replicas: 2\n"

func TestKubernetesPreviewBindsDiffIdentityAndPolicy(t *testing.T) {
	engine, database, actors, invocations := kubernetesPlanTestEngine(t)
	handler := NewServer(engine, database)
	request := model.KubernetesPlanRequest{Target: engine.catalog.Kubernetes["cluster-a"], Manifest: kubernetesPreviewManifest, IdempotencyKey: mustUUID(t)}
	plan := postKubernetesJSON[model.KubernetesPlan](t, handler, actors[0], "/v1/kubernetes/plans", request, http.StatusCreated)
	if plan.Preview == nil || !plan.Preview.HasChanges || len(plan.Preview.Resources) != 1 || plan.Preview.Resources[0].UID == "" || plan.PlanDigest == plan.ManifestDigest {
		t.Fatalf("预览证据不完整: %+v", plan)
	}
	before, _ := os.ReadFile(invocations)
	t.Setenv("KUBECTL_RV", "changed")
	replayed := postKubernetesJSON[model.KubernetesPlan](t, handler, actors[0], "/v1/kubernetes/plans", request, http.StatusOK)
	after, _ := os.ReadFile(invocations)
	if replayed.PlanDigest != plan.PlanDigest || string(before) != string(after) {
		t.Fatal("幂等重放重新采集或改写了预览")
	}
	postKubernetesJSON[map[string]any](t, handler, actors[1], "/v1/kubernetes/plans/"+plan.ID+"/approve",
		model.KubernetesPlanApprovalRequest{Digest: plan.PlanDigest, Confirmation: plan.ConfirmationPhrase}, http.StatusConflict)
}

func TestKubernetesPreviewDriftBlocksExecution(t *testing.T) {
	for _, variable := range []string{"KUBECTL_RV", "KUBECTL_UID", "KUBECTL_DIFF_VALUE", "KUBECTL_SERVER"} {
		t.Run(variable, func(t *testing.T) {
			engine, database, actors, invocation := kubernetesPlanTestEngine(t)
			handler := NewServer(engine, database)
			plan := createApprovedKubernetesPlan(t, handler, actors, engine.catalog.Kubernetes["cluster-a"], kubernetesPreviewManifest)
			t.Setenv(variable, "changed")
			postKubernetesJSON[map[string]any](t, handler, actors[0], "/v1/kubernetes/plans/"+plan.ID+"/execute",
				model.KubernetesPlanExecuteRequest{IdempotencyKey: mustUUID(t)}, http.StatusConflict)
			log, _ := os.ReadFile(invocation)
			if len(kubernetesMutationInvocations(string(log))) != 0 {
				t.Fatal("预览漂移后仍执行了 apply")
			}
		})
	}
}

func TestKubernetesDiffExitCodesAndTemporaryPaths(t *testing.T) {
	for _, code := range []string{"0", "1", "2"} {
		t.Run(code, func(t *testing.T) {
			engine, _, _, _ := kubernetesPlanTestEngine(t)
			t.Setenv("KUBECTL_DIFF_EXIT", code)
			preview, err := collectKubernetesPreview(context.Background(), engine.catalog.Kubernetes["cluster-a"], kubernetesPreviewManifest)
			if code == "2" {
				if err == nil {
					t.Fatal("diff 错误被接受")
				}
				return
			}
			if err != nil || preview.HasChanges != (code == "1") {
				t.Fatalf("preview=%+v err=%v", preview, err)
			}
		})
	}
	first := normalizeKubernetesDiff("--- /tmp/LIVE-one/object\n+++ /tmp/MERGED-one/object\n@@ -1 +1 @@\n-x: a\n+x: b")
	second := normalizeKubernetesDiff("--- /other/LIVE-two/object\n+++ /other/MERGED-two/object\n@@ -1 +1 @@\n-x: a\n+x: b")
	if first != second {
		t.Fatal("临时路径影响了差异摘要")
	}
}

func TestKubernetesStructuredDiffHidesEmbeddedAndNumericSecrets(t *testing.T) {
	before := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "app-a", "namespace": "ns-a"},
		"data": map[string]any{"credentials.yaml": "password: 991133557799\nsecret-key-before-colon: something"}}
	after := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "app-a", "namespace": "ns-a"},
		"data": map[string]any{"credentials.yaml": "password: 880022446688\nsecret-key-before-colon: other"}}
	raw := testKubernetesFullDiff(before, after)
	display, digest, err := kubernetesDiffEvidence(raw, []model.KubernetesResourceIdentity{{APIVersion: "v1", Kind: "ConfigMap", Name: "app-a", Namespace: "ns-a"}})
	if err != nil || digest == "" || display == "" {
		t.Fatalf("display=%s digest=%s err=%v", display, digest, err)
	}
	for _, secret := range []string{"991133557799", "880022446688", "secret-key-before-colon"} {
		if strings.Contains(display, secret) {
			t.Fatalf("公开差异泄漏 %s", secret)
		}
	}
}

func testKubernetesFullDiff(before, after any) string {
	left, _ := json.MarshalIndent(before, "", "  ")
	right, _ := json.MarshalIndent(after, "", "  ")
	return "diff -u -N LIVE/object MERGED/object\n--- LIVE/object\n+++ MERGED/object\n@@ -1,1 +1,1 @@\n-" +
		strings.ReplaceAll(string(left), "\n", "\n-") + "\n+" + strings.ReplaceAll(string(right), "\n", "\n+") + "\n"
}

func TestKubernetesCreateOnlyRejectsAnObjectAppearingAfterPreview(t *testing.T) {
	engine, database, actors, invocations := kubernetesPlanTestEngine(t)
	t.Setenv("KUBECTL_MISSING", "1")
	handler := NewServer(engine, database)
	plan := createApprovedKubernetesPlan(t, handler, actors, engine.catalog.Kubernetes["cluster-a"], kubernetesPreviewManifest)
	t.Setenv("KUBECTL_OBJECT_APPEARED", "1")
	postKubernetesJSON[map[string]any](t, handler, actors[0], "/v1/kubernetes/plans/"+plan.ID+"/execute",
		model.KubernetesPlanExecuteRequest{IdempotencyKey: mustUUID(t)}, http.StatusConflict)
	log, _ := os.ReadFile(invocations)
	mutations := kubernetesMutationInvocations(string(log))
	if len(mutations) != 1 || !strings.Contains(mutations[0], " create ") {
		t.Fatalf("缺席对象没有使用 create-only: %q", mutations)
	}
}

func TestKubernetesFailedRolloutCanCreateNewApprovedRollback(t *testing.T) {
	engine, database, actors, invocation := kubernetesPlanTestEngine(t)
	handler := NewServer(engine, database)
	target := engine.catalog.Kubernetes["cluster-a"]
	baseline := createAndExecuteKubernetesPlan(t, handler, actors, target, strings.Replace(kubernetesPreviewManifest, "replicas: 2", "replicas: 1", 1))
	source := createApprovedKubernetesPlan(t, handler, actors, target, kubernetesPreviewManifest)
	t.Setenv("KUBECTL_FAIL_ROLLOUT", "1")
	postKubernetesJSON[map[string]any](t, handler, actors[0], "/v1/kubernetes/plans/"+source.ID+"/execute",
		model.KubernetesPlanExecuteRequest{IdempotencyKey: mustUUID(t)}, http.StatusConflict)
	t.Setenv("KUBECTL_FAIL_ROLLOUT", "0")
	rollback := postKubernetesJSON[model.KubernetesPlan](t, handler, actors[0], "/v1/kubernetes/plans/"+source.ID+"/rollback",
		model.KubernetesRollbackPlanRequest{RollbackToPlanID: baseline.ID, IdempotencyKey: mustUUID(t)}, http.StatusCreated)
	if rollback.ID == source.ID || rollback.Preview == nil || rollback.Preview.SourceOperationID == "" {
		t.Fatal("失败恢复没有生成独立且绑定来源的新计划")
	}
	postKubernetesJSON[map[string]any](t, handler, actors[0], "/v1/kubernetes/plans/"+rollback.ID+"/execute",
		model.KubernetesPlanExecuteRequest{IdempotencyKey: mustUUID(t)}, http.StatusConflict)
	postKubernetesJSON[model.KubernetesPlan](t, handler, actors[1], "/v1/kubernetes/plans/"+rollback.ID+"/approve",
		model.KubernetesPlanApprovalRequest{Digest: rollback.PlanDigest, Confirmation: rollback.ConfirmationPhrase}, http.StatusOK)
	postKubernetesJSON[map[string]any](t, handler, actors[0], "/v1/kubernetes/plans/"+rollback.ID+"/execute",
		model.KubernetesPlanExecuteRequest{IdempotencyKey: mustUUID(t)}, http.StatusAccepted)
	original, _ := database.GetKubernetesPlan(context.Background(), source.ID)
	log, _ := os.ReadFile(invocation)
	if original.State != "needs_attention" || len(kubernetesMutationInvocations(string(log))) != 6 {
		t.Fatalf("旧失败证据被改写或发生额外执行: %+v", original)
	}
}

func TestKubernetesUnknownApplyCannotCreateRollback(t *testing.T) {
	engine, database, actors, _ := kubernetesPlanTestEngine(t)
	handler := NewServer(engine, database)
	target := engine.catalog.Kubernetes["cluster-a"]
	baseline := createAndExecuteKubernetesPlan(t, handler, actors, target, strings.Replace(kubernetesPreviewManifest, "replicas: 2", "replicas: 1", 1))
	source := createApprovedKubernetesPlan(t, handler, actors, target, kubernetesPreviewManifest)
	t.Setenv("KUBECTL_FAIL_APPLY", "1")
	postKubernetesJSON[map[string]any](t, handler, actors[0], "/v1/kubernetes/plans/"+source.ID+"/execute",
		model.KubernetesPlanExecuteRequest{IdempotencyKey: mustUUID(t)}, http.StatusConflict)
	postKubernetesJSON[map[string]any](t, handler, actors[0], "/v1/kubernetes/plans/"+source.ID+"/rollback",
		model.KubernetesRollbackPlanRequest{RollbackToPlanID: baseline.ID, IdempotencyKey: mustUUID(t)}, http.StatusConflict)
}

func TestGuardedKubernetesManifestBindsServerIdentity(t *testing.T) {
	guarded, err := guardedKubernetesManifest(kubernetesPreviewManifest, []model.KubernetesResourceIdentity{
		{APIVersion: "apps/v1", Kind: "Deployment", Name: "app-a", Namespace: "ns-a", Exists: true, UID: "uid-a", ResourceVersion: "42"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(guarded), &decoded); err != nil {
		t.Fatal(err)
	}
	meta := decoded["metadata"].(map[string]any)
	if meta["uid"] != "uid-a" || meta["resourceVersion"] != "42" {
		t.Fatal("未附加服务器身份前置条件")
	}
}
