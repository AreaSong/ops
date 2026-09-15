package runner

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestKubernetesConcurrentExecutionReplayKeepsActiveOperation(t *testing.T) {
	engine, database, actors, invocations := kubernetesPlanTestEngine(t)
	handler := NewServer(engine, database)
	plan := createApprovedKubernetesPlan(t, handler, actors, engine.catalog.Kubernetes["cluster-a"], kubernetesPreviewManifest)
	key := mustUUID(t)
	gate := filepath.Join(t.TempDir(), "mutation")
	t.Setenv("KUBECTL_MUTATION_GATE", gate)
	first := startKubernetesRequest(handler, actors[0], plan.ID, key)
	t.Cleanup(func() {
		_ = os.WriteFile(gate+".released", nil, 0o600)
		engine.Stop()
		engine.Wait()
	})
	waitKubernetesMutation(t, gate)
	replayed := postKubernetesJSON[struct {
		Operation model.KubernetesOperation `json:"operation"`
	}](t, handler, actors[0], "/v1/kubernetes/plans/"+plan.ID+"/execute",
		model.KubernetesPlanExecuteRequest{IdempotencyKey: key}, http.StatusAccepted)
	stored, err := database.GetKubernetesPlan(context.Background(), plan.ID)
	if err != nil || stored.State != "running" || replayed.Operation.State != "running" {
		t.Fatalf("活跃请求被误判为中断: plan=%+v operation=%+v err=%v", stored, replayed.Operation, err)
	}
	if err := os.WriteFile(gate+".released", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-first:
		if result.Code != http.StatusAccepted {
			t.Fatalf("首次执行失败: status=%d body=%s", result.Code, result.Body.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("首次执行未收口")
	}
	stored, err = database.GetKubernetesPlan(context.Background(), plan.ID)
	log, readErr := os.ReadFile(invocations)
	if err != nil || readErr != nil || stored.State != "succeeded" || len(kubernetesMutationInvocations(string(log))) != 2 {
		t.Fatalf("发生重复执行或终态错误: plan=%+v log=%s err=%v/%v", stored, log, err, readErr)
	}
	assertKubernetesPlanAudit(t, database, plan.ID)
}

func startKubernetesRequest(handler http.Handler, actor, planID, key string) <-chan *httptest.ResponseRecorder {
	result := make(chan *httptest.ResponseRecorder, 1)
	data, _ := json.Marshal(model.KubernetesPlanExecuteRequest{IdempotencyKey: key})
	request := httptest.NewRequest(http.MethodPost, "/v1/kubernetes/plans/"+planID+"/execute", bytes.NewReader(data))
	request.Header.Set(actorHeader, actor)
	request.Header.Set("Content-Type", "application/json")
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		result <- response
	}()
	return result
}

func waitKubernetesMutation(t *testing.T, gate string) {
	t.Helper()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("未进入假 kubectl mutation")
		case <-ticker.C:
			if _, err := os.Stat(gate + ".started"); err == nil {
				return
			}
		}
	}
}

func TestKubernetesAPIRejectsLegacyAndExpiredPreviewBeforeKubectl(t *testing.T) {
	for _, invalid := range []string{"legacy", "expired"} {
		for _, action := range []string{"approve", "execute"} {
			t.Run(invalid+"/"+action, func(t *testing.T) {
				engine, database, actors, invocations := kubernetesPlanTestEngine(t)
				handler := NewServer(engine, database)
				plan := postKubernetesJSON[model.KubernetesPlan](t, handler, actors[0], "/v1/kubernetes/plans",
					model.KubernetesPlanRequest{Target: engine.catalog.Kubernetes["cluster-a"], Manifest: kubernetesPreviewManifest, IdempotencyKey: mustUUID(t)}, http.StatusCreated)
				if action == "execute" {
					plan = postKubernetesJSON[model.KubernetesPlan](t, handler, actors[1], "/v1/kubernetes/plans/"+plan.ID+"/approve",
						model.KubernetesPlanApprovalRequest{Digest: plan.PlanDigest, Confirmation: plan.ConfirmationPhrase}, http.StatusOK)
				}
				invalidateKubernetesTestPreview(t, engine, &plan, invalid)
				before, _ := os.ReadFile(invocations)
				if action == "approve" {
					postKubernetesJSON[map[string]any](t, handler, actors[1], "/v1/kubernetes/plans/"+plan.ID+"/approve",
						model.KubernetesPlanApprovalRequest{Digest: plan.PlanDigest, Confirmation: plan.ConfirmationPhrase}, http.StatusConflict)
				} else {
					postKubernetesJSON[map[string]any](t, handler, actors[0], "/v1/kubernetes/plans/"+plan.ID+"/execute",
						model.KubernetesPlanExecuteRequest{IdempotencyKey: mustUUID(t)}, http.StatusConflict)
				}
				after, _ := os.ReadFile(invocations)
				stored, err := database.GetKubernetesPlan(context.Background(), plan.ID)
				if err != nil || string(before) != string(after) || stored.State != plan.State || stored.OperationID != "" {
					t.Fatalf("失效预览仍调用 kubectl 或改变状态: %+v err=%v", stored, err)
				}
			})
		}
	}
}

func invalidateKubernetesTestPreview(t *testing.T, engine *Engine, plan *model.KubernetesPlan, invalid string) {
	t.Helper()
	if invalid == "legacy" {
		plan.Preview, plan.PlanDigest = nil, plan.ManifestDigest
	} else {
		plan.Preview.ObservedAt = time.Now().UTC().Add(-16 * time.Minute)
		plan.Preview.ExpiresAt = plan.Preview.ObservedAt.Add(15 * time.Minute)
		var err error
		plan.PlanDigest, err = plan.ApprovalDigest()
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal(plan.Preview)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", filepath.Join(engine.stateRoot, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`UPDATE kubernetes_plans SET preview_json=?,plan_digest=? WHERE id=?`, string(data), plan.PlanDigest, plan.ID); err != nil {
		t.Fatal(err)
	}
}
