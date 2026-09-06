package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestKubernetesPlanAPIDualApprovalAndIdempotentExecution(t *testing.T) {
	engine, database, actors, invocationFile := kubernetesPlanTestEngine(t)
	handler := NewServer(engine, database)
	target := engine.catalog.Kubernetes["cluster-a"]
	manifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: app-a\n  namespace: ns-a\nspec:\n  replicas: 2\n"

	created := postKubernetesJSON[model.KubernetesPlan](t, handler, actors[0], "/v1/kubernetes/plans", model.KubernetesPlanRequest{
		Target: target, Manifest: manifest, IdempotencyKey: mustUUID(t),
	}, http.StatusCreated)
	if created.State != "pending_approval" || created.ManifestDigest == "" || created.ConfirmationPhrase == "" {
		t.Fatalf("created plan=%+v", created)
	}

	postKubernetesJSON[map[string]any](t, handler, actors[0], "/v1/kubernetes/plans/"+created.ID+"/approve", model.KubernetesPlanApprovalRequest{
		Digest: created.ManifestDigest, Confirmation: created.ConfirmationPhrase,
	}, http.StatusConflict)

	first := postKubernetesJSON[model.KubernetesPlan](t, handler, actors[1], "/v1/kubernetes/plans/"+created.ID+"/approve", model.KubernetesPlanApprovalRequest{
		Digest: created.ManifestDigest, Confirmation: created.ConfirmationPhrase,
	}, http.StatusOK)
	if first.State != "approved" || first.ApprovedByHash != actors[1] || first.ApprovalPolicy != model.ApprovalPolicyTwoParty {
		t.Fatalf("first approval=%+v", first)
	}

	executeKey := mustUUID(t)
	postKubernetesJSON[map[string]any](t, handler, actors[1], "/v1/kubernetes/plans/"+created.ID+"/execute", model.KubernetesPlanExecuteRequest{
		IdempotencyKey: executeKey,
	}, http.StatusConflict)
	firstExecution := postKubernetesJSON[struct {
		Operation model.KubernetesOperation `json:"operation"`
	}](t, handler, actors[0], "/v1/kubernetes/plans/"+created.ID+"/execute", model.KubernetesPlanExecuteRequest{
		IdempotencyKey: executeKey,
	}, http.StatusAccepted)
	if firstExecution.Operation.State != "succeeded" || firstExecution.Operation.Action != "apply" || firstExecution.Operation.DryRun ||
		firstExecution.Operation.ID != "plan-"+created.ID ||
		firstExecution.Operation.Phase != "health" || firstExecution.Operation.RolloutState != "succeeded" ||
		len(firstExecution.Operation.RolloutResources) != 1 || firstExecution.Operation.RolloutResources[0] != "deployment/app-a" {
		t.Fatalf("first execution=%+v", firstExecution.Operation)
	}

	replayed := postKubernetesJSON[struct {
		Operation model.KubernetesOperation `json:"operation"`
	}](t, handler, actors[0], "/v1/kubernetes/plans/"+created.ID+"/execute", model.KubernetesPlanExecuteRequest{
		IdempotencyKey: executeKey,
	}, http.StatusAccepted)
	if replayed.Operation.ID != firstExecution.Operation.ID {
		t.Fatalf("replayed operation=%s want=%s", replayed.Operation.ID, firstExecution.Operation.ID)
	}
	postKubernetesJSON[map[string]any](t, handler, actors[2], "/v1/kubernetes/plans/"+created.ID+"/execute", model.KubernetesPlanExecuteRequest{
		IdempotencyKey: mustUUID(t),
	}, http.StatusConflict)

	invocations, err := os.ReadFile(invocationFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(invocations)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "--context ctx-a -n ns-a apply --field-manager areasong-ops -f -") ||
		!strings.Contains(lines[1], "--context ctx-a -n ns-a rollout status deployment/app-a --timeout=5m") {
		t.Fatalf("kubectl invocations=%q", string(invocations))
	}
	stored, err := database.GetKubernetesPlan(context.Background(), created.ID)
	if err != nil || stored.State != "succeeded" || stored.OperationID != firstExecution.Operation.ID ||
		stored.ExecuteIdempotencyKey != executeKey || stored.ExecutedByHash != actors[0] {
		t.Fatalf("stored plan=%+v err=%v", stored, err)
	}
	assertKubernetesPlanAudit(t, database, created.ID)
}

func assertKubernetesPlanAudit(t *testing.T, database *store.Store, planID string) {
	t.Helper()
	entries, err := database.ListAudit(context.Background(), 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"kubernetes.plan.created": 1, "kubernetes.plan.approved": 1,
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

func TestKubernetesRollbackPlanBindsSourceAndRunsRollout(t *testing.T) {
	engine, database, actors, invocationFile := kubernetesPlanTestEngine(t)
	handler := NewServer(engine, database)
	target := engine.catalog.Kubernetes["cluster-a"]
	rollbackManifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: app-a\n  namespace: ns-a\nspec:\n  replicas: 1\n"
	baseline := createAndExecuteKubernetesPlan(t, handler, actors, target, rollbackManifest)
	currentManifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: app-a\n  namespace: ns-a\nspec:\n  replicas: 2\n"
	source := createAndExecuteKubernetesPlan(t, handler, actors, target, currentManifest)

	postKubernetesJSON[map[string]any](t, handler, actors[4], "/v1/kubernetes/plans/"+source.ID+"/rollback", model.KubernetesRollbackPlanRequest{
		RollbackToPlanID: baseline.ID, IdempotencyKey: mustUUID(t),
	}, http.StatusForbidden)
	postKubernetesJSON[map[string]any](t, handler, actors[0], "/v1/kubernetes/plans/"+source.ID+"/rollback", map[string]any{
		"manifest": rollbackManifest, "idempotencyKey": mustUUID(t),
	}, http.StatusBadRequest)
	rollback := postKubernetesJSON[model.KubernetesPlan](t, handler, actors[0], "/v1/kubernetes/plans/"+source.ID+"/rollback", model.KubernetesRollbackPlanRequest{
		RollbackToPlanID: baseline.ID, IdempotencyKey: mustUUID(t),
	}, http.StatusCreated)
	if rollback.Action != "rollback" || rollback.RollbackOfPlanID != source.ID ||
		rollback.RollbackTargetPlanID != baseline.ID || rollback.SourceManifestDigest != source.ManifestDigest ||
		rollback.ManifestDigest != baseline.ManifestDigest || rollback.ManifestDigest == source.ManifestDigest {
		t.Fatalf("rollback plan=%+v", rollback)
	}
	rollback = postKubernetesJSON[model.KubernetesPlan](t, handler, actors[1], "/v1/kubernetes/plans/"+rollback.ID+"/approve", model.KubernetesPlanApprovalRequest{
		Digest: rollback.ManifestDigest, Confirmation: rollback.ConfirmationPhrase,
	}, http.StatusOK)
	result := postKubernetesJSON[struct {
		Operation model.KubernetesOperation `json:"operation"`
	}](t, handler, actors[0], "/v1/kubernetes/plans/"+rollback.ID+"/execute", model.KubernetesPlanExecuteRequest{
		IdempotencyKey: mustUUID(t),
	}, http.StatusAccepted)
	if result.Operation.State != "succeeded" || result.Operation.Action != "rollback" ||
		result.Operation.RollbackOfPlanID != source.ID || result.Operation.RolloutState != "succeeded" {
		t.Fatalf("rollback operation=%+v", result.Operation)
	}
	invocations, err := os.ReadFile(invocationFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(invocations)), "\n")
	if len(lines) != 6 || !strings.Contains(lines[4], " apply ") ||
		!strings.Contains(lines[5], "rollout status deployment/app-a") {
		t.Fatalf("rollback kubectl invocations=%q", string(invocations))
	}
}

func TestKubernetesRolloutFailureNeedsAttention(t *testing.T) {
	engine, database, actors, _ := kubernetesPlanTestEngine(t)
	t.Setenv("KUBECTL_FAIL_ROLLOUT", "1")
	handler := NewServer(engine, database)
	manifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: app-a\n  namespace: ns-a\n"
	plan := createApprovedKubernetesPlan(t, handler, actors, engine.catalog.Kubernetes["cluster-a"], manifest)
	response := postKubernetesJSON[map[string]any](t, handler, actors[0], "/v1/kubernetes/plans/"+plan.ID+"/execute", model.KubernetesPlanExecuteRequest{
		IdempotencyKey: mustUUID(t),
	}, http.StatusConflict)
	if response["error"] == nil {
		t.Fatalf("missing rollout failure: %+v", response)
	}
	stored, err := database.GetKubernetesPlan(context.Background(), plan.ID)
	if err != nil || stored.State != "needs_attention" {
		t.Fatalf("stored plan=%+v err=%v", stored, err)
	}
	operation, _, err := database.GetKubernetesOperation(context.Background(), stored.OperationID)
	if err != nil || operation.State != "needs_attention" || operation.Phase != "rollout_failed" || operation.RolloutState != "failed" {
		t.Fatalf("stored operation=%+v err=%v", operation, err)
	}
}

func createAndExecuteKubernetesPlan(
	t *testing.T,
	handler http.Handler,
	actors []string,
	target model.KubernetesTarget,
	manifest string,
) model.KubernetesPlan {
	t.Helper()
	plan := createApprovedKubernetesPlan(t, handler, actors, target, manifest)
	postKubernetesJSON[struct {
		Operation model.KubernetesOperation `json:"operation"`
	}](t, handler, actors[0], "/v1/kubernetes/plans/"+plan.ID+"/execute", model.KubernetesPlanExecuteRequest{
		IdempotencyKey: mustUUID(t),
	}, http.StatusAccepted)
	return plan
}

func createApprovedKubernetesPlan(
	t *testing.T,
	handler http.Handler,
	actors []string,
	target model.KubernetesTarget,
	manifest string,
) model.KubernetesPlan {
	t.Helper()
	plan := postKubernetesJSON[model.KubernetesPlan](t, handler, actors[0], "/v1/kubernetes/plans", model.KubernetesPlanRequest{
		Target: target, Manifest: manifest, IdempotencyKey: mustUUID(t),
	}, http.StatusCreated)
	plan = postKubernetesJSON[model.KubernetesPlan](t, handler, actors[1], "/v1/kubernetes/plans/"+plan.ID+"/approve", model.KubernetesPlanApprovalRequest{
		Digest: plan.ManifestDigest, Confirmation: plan.ConfirmationPhrase,
	}, http.StatusOK)
	return plan
}

func kubernetesPlanTestEngine(t *testing.T) (*Engine, *store.Store, []string, string) {
	t.Helper()
	root := t.TempDir()
	database, err := store.Open(filepath.Join(root, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	invocationFile := filepath.Join(root, "kubectl-invocations")
	kubectl := filepath.Join(binDir, "kubectl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$KUBECTL_INVOCATIONS\"\ncat >/dev/null\nif [ \"$5\" = rollout ] && [ \"${KUBECTL_FAIL_ROLLOUT:-0}\" = 1 ]; then printf 'rollout failed\\n' >&2; exit 1; fi\nprintf 'ok\\n'\n"
	if err := os.WriteFile(kubectl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KUBECTL_INVOCATIONS", invocationFile)

	actors := []string{
		config.AccessHashForEmail("creator@example.test"),
		config.AccessHashForEmail("approver-one@example.test"),
		config.AccessHashForEmail("approver-two@example.test"),
		config.AccessHashForEmail("executor@example.test"),
		config.AccessHashForEmail("other-tenant@example.test"),
	}
	now := time.Now().UTC()
	principals := make(map[string]config.AccessPrincipal, len(actors))
	bindings := make([]model.RoleBinding, 0, len(actors))
	for index, actor := range actors {
		tenantID := "tenant-a"
		if index == 4 {
			tenantID = "tenant-b"
		}
		principals[actor] = config.AccessPrincipal{Subject: actor, TenantID: tenantID}
		bindings = append(bindings, model.RoleBinding{
			ID: "kube-operator-" + string(rune('a'+index)), Subject: actor,
			TenantID: tenantID, RoleID: "kube-operator", ObjectIDs: []string{"*"},
		})
	}
	catalog := &config.Catalog{
		SchemaVersion: 4,
		Services:      map[string]model.ServiceDefinition{},
		Access: &config.AccessPolicy{
			Enforced: true, DefaultTenant: "tenant-a",
			Tenants: map[string]model.Tenant{
				"tenant-a": {ID: "tenant-a", DisplayName: "Tenant A", Status: "active", CreatedAt: now},
				"tenant-b": {ID: "tenant-b", DisplayName: "Tenant B", Status: "active", CreatedAt: now},
			},
			Roles: map[string]model.Role{
				"kube-operator": {ID: "kube-operator", DisplayName: "Kubernetes Operator", Permissions: []model.Permission{model.PermissionDeploy}},
			},
			Principals: principals,
			Bindings:   bindings,
		},
		Kubernetes: map[string]model.KubernetesTarget{
			"cluster-a": {
				Cluster: "cluster-a", Context: "ctx-a", Namespace: "ns-a", TenantID: "tenant-a",
				Allowlist: []string{"deployment/app-a"}, ResourceKinds: []string{"Deployment"},
			},
		},
	}
	engine, err := NewEngineChecked(catalog, database, &fakeExecutor{}, root)
	if err != nil {
		t.Fatal(err)
	}
	return engine, database, actors, invocationFile
}

func postKubernetesJSON[T any](
	t *testing.T,
	handler http.Handler,
	actor, path string,
	body any,
	wantStatus int,
) T {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	request.Header.Set(actorHeader, actor)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("POST %s status=%d want=%d body=%s", path, response.Code, wantStatus, response.Body.String())
	}
	var result T
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode POST %s response: %v body=%s", path, err, response.Body.String())
	}
	return result
}
