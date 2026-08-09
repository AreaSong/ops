package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

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
