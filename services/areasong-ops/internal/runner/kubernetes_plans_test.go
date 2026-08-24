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
	manifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: app-a\n  namespace: ns-a\n"

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
	if first.State != "pending_approval" || first.ApprovedByHash != actors[1] {
		t.Fatalf("first approval=%+v", first)
	}
	second := postKubernetesJSON[model.KubernetesPlan](t, handler, actors[2], "/v1/kubernetes/plans/"+created.ID+"/approve", model.KubernetesPlanApprovalRequest{
		Digest: created.ManifestDigest, Confirmation: created.ConfirmationPhrase,
	}, http.StatusOK)
	if second.State != "approved" || second.SecondApprovedByHash != actors[2] {
		t.Fatalf("second approval=%+v", second)
	}

	executeKey := mustUUID(t)
	postKubernetesJSON[map[string]any](t, handler, actors[2], "/v1/kubernetes/plans/"+created.ID+"/execute", model.KubernetesPlanExecuteRequest{
		IdempotencyKey: executeKey,
	}, http.StatusConflict)

	firstExecution := postKubernetesJSON[struct {
		Operation model.KubernetesOperation `json:"operation"`
	}](t, handler, actors[3], "/v1/kubernetes/plans/"+created.ID+"/execute", model.KubernetesPlanExecuteRequest{
		IdempotencyKey: executeKey,
	}, http.StatusAccepted)
	if firstExecution.Operation.State != "succeeded" || firstExecution.Operation.Action != "apply" || firstExecution.Operation.DryRun {
		t.Fatalf("first execution=%+v", firstExecution.Operation)
	}

	replayed := postKubernetesJSON[struct {
		Operation model.KubernetesOperation `json:"operation"`
	}](t, handler, actors[3], "/v1/kubernetes/plans/"+created.ID+"/execute", model.KubernetesPlanExecuteRequest{
		IdempotencyKey: executeKey,
	}, http.StatusAccepted)
	if replayed.Operation.ID != firstExecution.Operation.ID {
		t.Fatalf("replayed operation=%s want=%s", replayed.Operation.ID, firstExecution.Operation.ID)
	}
	postKubernetesJSON[map[string]any](t, handler, actors[3], "/v1/kubernetes/plans/"+created.ID+"/execute", model.KubernetesPlanExecuteRequest{
		IdempotencyKey: mustUUID(t),
	}, http.StatusConflict)

	invocations, err := os.ReadFile(invocationFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(invocations)), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "--context ctx-a -n ns-a apply --field-manager areasong-ops -f -") {
		t.Fatalf("kubectl invocations=%q", string(invocations))
	}
	stored, err := database.GetKubernetesPlan(context.Background(), created.ID)
	if err != nil || stored.State != "succeeded" || stored.OperationID != firstExecution.Operation.ID || stored.ExecuteIdempotencyKey != executeKey {
		t.Fatalf("stored plan=%+v err=%v", stored, err)
	}
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
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$KUBECTL_INVOCATIONS\"\ncat >/dev/null\nprintf 'applied\\n'\n"
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
	}
	now := time.Now().UTC()
	principals := make(map[string]config.AccessPrincipal, len(actors))
	bindings := make([]model.RoleBinding, 0, len(actors))
	for index, actor := range actors {
		principals[actor] = config.AccessPrincipal{Subject: actor, TenantID: "tenant-a"}
		bindings = append(bindings, model.RoleBinding{
			ID: "kube-operator-" + string(rune('a'+index)), Subject: actor,
			TenantID: "tenant-a", RoleID: "kube-operator", ObjectIDs: []string{"*"},
		})
	}
	catalog := &config.Catalog{
		SchemaVersion: 4,
		Services:      map[string]model.ServiceDefinition{},
		Access: &config.AccessPolicy{
			Enforced: true, DefaultTenant: "tenant-a",
			Tenants: map[string]model.Tenant{
				"tenant-a": {ID: "tenant-a", DisplayName: "Tenant A", Status: "active", CreatedAt: now},
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
