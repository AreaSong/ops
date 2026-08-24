package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildExternalLinks(t *testing.T) {
	links, err := buildExternalLinks("https://monitor.example.com/", false)
	if err != nil {
		t.Fatal(err)
	}
	if links.Grafana != "https://monitor.example.com" || links.Alerts != "https://monitor.example.com/alerting/list" {
		t.Fatalf("links=%+v", links)
	}

	for _, value := range []string{
		"http://monitor.example.com",
		"https://user@example.com",
		"https://monitor.example.com/path",
		"https://monitor.example.com?token=value",
	} {
		if _, err := buildExternalLinks(value, false); err == nil {
			t.Fatalf("expected invalid Grafana URL: %s", value)
		}
	}
	if _, err := buildExternalLinks("http://127.0.0.1:3000", true); err != nil {
		t.Fatalf("development URL rejected: %v", err)
	}
}

func TestSessionReturnsConfiguredExternalLinks(t *testing.T) {
	server := &Server{
		auth: DevelopmentAuthenticator{Email: "operator@example.com"},
		externalLinks: ExternalLinks{
			Grafana: "https://monitor.example.com",
			Alerts:  "https://monitor.example.com/alerting/list",
		},
		development: true,
	}
	request := httptest.NewRequest(http.MethodGet, "https://ops.example.com/api/session", nil)
	response := httptest.NewRecorder()
	server.session(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Email string        `json:"email"`
		Links ExternalLinks `json:"links"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Email != "operator@example.com" || payload.Links != server.externalLinks {
		t.Fatalf("payload=%+v", payload)
	}
}

func TestAPIForwardsOnlyHashedActorToRunner(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "areasong-ops-web-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "runner.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	actorHeaders := make(chan string, 1)
	runnerServer := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		actorHeaders <- request.Header.Get(actorHeader)
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"services":[]}`))
	})}
	go runnerServer.Serve(listener)
	defer runnerServer.Close()

	server := &Server{
		auth:   DevelopmentAuthenticator{Email: "song80184@gmail.com"},
		runner: NewRunnerClient(socket), publicOrigin: "https://ops.areasong.top",
		development: true,
	}
	request := httptest.NewRequest(http.MethodGet, "https://ops.areasong.top/api/services", nil)
	response := httptest.NewRecorder()
	server.api(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	expected := sha256.Sum256([]byte("song80184@gmail.com"))
	if actual := <-actorHeaders; actual != hex.EncodeToString(expected[:]) {
		t.Fatalf("actor hash=%q", actual)
	}
	if response.Body.String() == "song80184@gmail.com" {
		t.Fatal("response unexpectedly exposed actor email")
	}
}

func TestMutationRequiresMatchingCSRFCookie(t *testing.T) {
	server := &Server{
		auth:         DevelopmentAuthenticator{Email: "song80184@gmail.com"},
		runner:       NewRunnerClient(filepath.Join(t.TempDir(), "missing.sock")),
		publicOrigin: "https://ops.areasong.top", development: false,
	}
	request := httptest.NewRequest(http.MethodPost, "https://ops.areasong.top/api/tasks", nil)
	request.Header.Set("Origin", "https://ops.areasong.top")
	request.Header.Set("X-AreaSong-Ops-CSRF", "wrong")
	request.AddCookie(&http.Cookie{Name: csrfCookie, Value: "expected"})
	response := httptest.NewRecorder()
	server.api(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRunnerClientRejectsRemoteEndpoint(t *testing.T) {
	if client, err := NewRemoteRunnerClient("https://runner.example.test"); err == nil || client != nil {
		t.Fatalf("remote Runner client=%v err=%v", client, err)
	}
}
