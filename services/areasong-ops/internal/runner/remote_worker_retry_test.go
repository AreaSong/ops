package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestRemoteWorkerRetryClassificationAndDelay(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusServiceUnavailable} {
		response := &http.Response{StatusCode: status, Status: http.StatusText(status)}
		if !isRetryableRemoteWorkerError(newRemoteWorkerHTTPError("test", response)) {
			t.Fatalf("status %d should be retryable", status)
		}
	}
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusConflict, http.StatusPreconditionFailed} {
		response := &http.Response{StatusCode: status, Status: http.StatusText(status)}
		if isRetryableRemoteWorkerError(newRemoteWorkerHTTPError("test", response)) {
			t.Fatalf("status %d should fail immediately", status)
		}
	}
	base := 250 * time.Millisecond
	if got := remoteWorkerRetryDelay(0, base); got != base {
		t.Fatalf("first delay=%s", got)
	}
	if got := remoteWorkerRetryDelay(3, base); got != 2*time.Second {
		t.Fatalf("fourth delay=%s", got)
	}
	if got := remoteWorkerRetryDelay(20, base); got != maxRemoteWorkerRetryInterval {
		t.Fatalf("capped delay=%s", got)
	}
}

func TestRemoteWorkerRunRetriesTemporaryControlPlaneFailure(t *testing.T) {
	var calls atomic.Int32
	succeeded := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/fleet/runners/runner-a/assignments/claim" {
			t.Errorf("unexpected path %s", request.URL.Path)
		}
		if calls.Add(1) == 1 {
			http.Error(response, "temporary", http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusNoContent)
		once.Do(func() { close(succeeded) })
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	worker := &RemoteWorker{
		RunnerID: "runner-a", Endpoint: server.URL, Client: server.Client(), Catalog: &config.Catalog{},
		Executor: &fakeExecutor{}, PollInterval: time.Millisecond,
	}
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case <-succeeded:
		cancel()
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("worker did not recover from temporary failure")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls.Load() < 2 {
		t.Fatalf("claim calls=%d", calls.Load())
	}
}

func TestRemoteWorkerRunStopsOnPermanentControlPlaneFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(response, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()
	worker := &RemoteWorker{
		RunnerID: "runner-a", Endpoint: server.URL, Client: server.Client(), Catalog: &config.Catalog{},
		Executor: &fakeExecutor{}, PollInterval: time.Millisecond,
	}
	if err := worker.Run(context.Background()); err == nil || isRetryableRemoteWorkerError(err) {
		t.Fatalf("permanent failure err=%v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("permanent failure calls=%d", calls.Load())
	}
}

func TestRemoteWorkerInitialHeartbeatRetriesTemporaryFailure(t *testing.T) {
	fixture := newFleetWorkerFixture(t)
	fixture.worker.PollInterval = time.Millisecond
	fixture.worker.Identity = func() (string, string, string, error) {
		return "v2", fixture.assignment.Manifest.ArtifactRevision,
			fixture.assignment.Manifest.ArtifactDigest, nil
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(response, "temporary", http.StatusServiceUnavailable)
			return
		}
		var input RunnerHeartbeatRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		_ = json.NewEncoder(response).Encode(model.RunnerNode{
			ID: fixture.worker.RunnerID, ServerID: "server-a", Version: input.Version,
			Revision: input.Revision, BinaryDigest: input.BinaryDigest,
			IdentityPayloadVersion: RunnerIdentityPayloadVersion, LeaseGeneration: 1, State: model.NodeOnline,
		})
	}))
	defer server.Close()
	fixture.worker.Endpoint, fixture.worker.Client = server.URL, server.Client()
	if err := fixture.worker.establishIdentityHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("heartbeat calls=%d", calls.Load())
	}
}

func TestRemoteWorkerIdentityReceiptMismatchRemainsPermanent(t *testing.T) {
	err := context.Canceled
	if isRetryableRemoteWorkerError(err) {
		t.Fatal("context cancellation must not be retried")
	}
	if isRetryableRemoteWorkerError(errors.New("identity mismatch")) {
		t.Fatal("identity mismatch must not be retried")
	}
}
