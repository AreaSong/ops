package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAlertmanagerClientUsesBoundedV2Contracts(t *testing.T) {
	t.Helper()
	var silencePayload map[string]any
	var expiredPath string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v2/alerts":
			if request.URL.Query().Get("active") != "true" || request.URL.Query().Get("silenced") != "false" ||
				request.URL.Query().Get("inhibited") != "false" {
				t.Fatalf("unexpected query: %s", request.URL.RawQuery)
			}
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`[{
  "annotations": {
    "summary": "应用不可用",
    "runbook_url": "https://github.com/AreaSong/ops/runbook",
    "grafana_url": "javascript:alert(1)"
  },
  "labels": {"alertname": "AppHttpProbeFailed", "service": "demo", "severity": "critical"},
  "startsAt": "2026-08-11T00:00:00Z",
  "fingerprint": "abcdef1234567890",
  "status": {"silencedBy": []}
}]`))
		case request.Method == http.MethodPost && request.URL.Path == "/api/v2/silences":
			if err := json.NewDecoder(request.Body).Decode(&silencePayload); err != nil {
				t.Fatal(err)
			}
			_, _ = response.Write([]byte(`{"silenceID":"silence-test-1"}`))
		case request.Method == http.MethodDelete && strings.HasPrefix(request.URL.Path, "/api/v2/silence/"):
			expiredPath = request.URL.Path
			response.WriteHeader(http.StatusOK)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client, err := NewAlertmanagerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	alerts, err := client.ListAlerts(context.Background(), false)
	if err != nil || len(alerts) != 1 || alerts[0].Fingerprint != "abcdef1234567890" ||
		alerts[0].GrafanaURL != "" || alerts[0].RunbookURL == "" {
		t.Fatalf("alerts=%+v err=%v", alerts, err)
	}
	now := time.Now().UTC()
	silence, err := client.CreateSilence(context.Background(), map[string]string{"service": "demo"},
		[]string{"BusinessHttpProbeFailed", "AppHttpProbeFailed"}, now, now.Add(time.Hour), "plan test")
	if err != nil || silence.ID != "silence-test-1" {
		t.Fatalf("silence=%+v err=%v", silence, err)
	}
	matchers, ok := silencePayload["matchers"].([]any)
	if !ok || len(matchers) != 2 {
		t.Fatalf("matchers=%#v", silencePayload["matchers"])
	}
	alertMatcher := matchers[1].(map[string]any)
	if alertMatcher["name"] != "alertname" || alertMatcher["isRegex"] != true ||
		alertMatcher["value"] != "^(?:AppHttpProbeFailed|BusinessHttpProbeFailed)$" {
		t.Fatalf("alert matcher=%#v", alertMatcher)
	}
	if err := client.ExpireSilence(context.Background(), silence.ID); err != nil {
		t.Fatal(err)
	}
	if expiredPath != "/api/v2/silence/silence-test-1" {
		t.Fatalf("expired path=%q", expiredPath)
	}
}

func TestAlertmanagerClientRejectsRemoteOrigin(t *testing.T) {
	if _, err := NewAlertmanagerClient("https://alertmanager.example.com"); err == nil {
		t.Fatal("expected remote Alertmanager origin to be rejected")
	}
}

func TestAlertmanagerClientTreatsMissingSilenceAsExpired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete || request.URL.Path != "/api/v2/silence/missing" {
			http.NotFound(response, request)
			return
		}
		http.NotFound(response, request)
	}))
	defer server.Close()

	client, err := NewAlertmanagerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ExpireSilence(context.Background(), "missing"); err != nil {
		t.Fatalf("missing silence should be idempotently expired: %v", err)
	}
}
