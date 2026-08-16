package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestMetricsExposeActiveCredentialRotationStateAndAge(t *testing.T) {
	engine, database := testEngine(t, &fakeExecutor{})
	rotation := model.CredentialRotation{
		ID: "metrics-endpoint-rotation", IdempotencyKey: "metrics-endpoint-key",
		ActorHash: "actor", CredentialType: model.GitHubAlertmanagerCredential,
		Target: "fixed target", State: model.CredentialRotationRunning,
		Fingerprint: "sha256:0123456789ab", ExpiresAt: "2027-08-12", CreatedAt: time.Now().UTC(),
	}
	if _, _, err := database.StartCredentialRotation(context.Background(), rotation); err != nil {
		t.Fatal(err)
	}
	if err := database.FinishCredentialRotation(context.Background(), rotation.ID,
		model.CredentialRotationResult{State: model.CredentialRotationSwitchedPendingRevocation}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	NewServer(engine, database).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("metrics status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, metric := range []string{
		`areasong_ops_credential_rotation_active{credential_type="github_alertmanager",state="switched_pending_revocation"} 1`,
		`areasong_ops_credential_rotation_age_seconds{credential_type="github_alertmanager",state="switched_pending_revocation"}`,
	} {
		if !strings.Contains(body, metric) {
			t.Fatalf("metrics missing %q in %s", metric, body)
		}
	}
}
