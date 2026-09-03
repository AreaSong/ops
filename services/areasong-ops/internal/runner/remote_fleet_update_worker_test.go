package runner

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

type fleetWorkerFixture struct {
	worker     *RemoteWorker
	db         *store.Store
	assignment model.FleetRunnerUpdateAssignment
	content    []byte
	publicKey  ed25519.PublicKey
}

func newFleetWorkerFixture(t *testing.T) *fleetWorkerFixture {
	t.Helper()
	root := t.TempDir()
	database, err := store.Open(filepath.Join(root, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("signed-fleet-runner-v2")
	key := base64.StdEncoding.EncodeToString(publicKey)
	catalog := &config.Catalog{
		SchemaVersion: 4,
		Fleet: &config.FleetPolicy{
			Enabled: true, HeartbeatTimeoutSeconds: 90, AllowRemoteRunners: true,
			RequiremTLS: true, RequireSignedHeartbeat: true,
			Inventory: model.Fleet{Runners: []model.RunnerNode{{
				ID: "runner-a", ServerID: "server-a", TenantID: "tenant-a", Version: "v1",
				State: model.NodeOnline, Capabilities: []string{"runner-update"},
				HeartbeatPublicKey: key,
			}}},
		},
		RunnerUpdate: &config.RunnerUpdatePolicy{
			Enabled: true, FleetEnabled: true, RunnerID: "runner-a",
			ArtifactRoot: filepath.Join(root, "runner-updates", "incoming"),
			BinaryPath:   filepath.Join(root, "runner"), UnitName: "areasong-ops-runner.service",
			UpdaterUnitName: "areasong-ops-runner-update@.service", Publisher: "release",
			ManifestGOOS: "linux", ManifestGOARCH: "amd64",
			TrustedPublisherKeys: map[string]string{"release": key},
			HealthTimeoutSeconds: 30, MaxArtifactBytes: 1 << 20,
		},
	}
	manifest := model.FleetRunnerUpdateManifest{
		Purpose: model.FleetRunnerUpdateManifestPurpose, Schema: model.FleetRunnerUpdateManifestSchema,
		GOOS: "linux", GOARCH: "amd64", TargetVersion: "v2",
		ArtifactDigest: digestText(string(content)), ArtifactRevision: strings.Repeat("b", 40),
		Publisher: "release",
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	assignment := model.FleetRunnerUpdateAssignment{
		PlanID:   "11111111-1111-4111-8111-111111111111",
		ItemID:   "22222222-2222-4222-8222-222222222222",
		RunnerID: "runner-a", ServerID: "server-a", Action: "update",
		Manifest: manifest, ArtifactSignature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)),
		PreviousVersion: "v1", PreviousRevision: strings.Repeat("a", 40),
		PreviousDigest:      "sha256:" + strings.Repeat("a", 64),
		PolicyDigest:        fleetRunnerUpdatePolicyDigest(catalog.RunnerUpdate, catalog.Fleet),
		PlanDigest:          "sha256:" + strings.Repeat("c", 64),
		ExecutionDeadlineAt: time.Now().UTC().Add(time.Hour),
		Fence:               model.FleetRunnerUpdateFence{Generation: 1, ClaimToken: "claim-token"},
	}
	worker := &RemoteWorker{
		RunnerID: "runner-a", Catalog: catalog, Store: database, StateRoot: root,
		Lease: time.Minute, Now: time.Now, HeartbeatPrivateKey: privateKey,
	}
	t.Cleanup(func() { _ = database.Close() })
	return &fleetWorkerFixture{
		worker: worker, db: database, assignment: assignment, content: content, publicKey: publicKey,
	}
}

func (fixture *fleetWorkerFixture) receipt(endpoint string) model.FleetRunnerUpdateReceipt {
	return model.FleetRunnerUpdateReceipt{
		ItemID: fixture.assignment.ItemID, AssignmentGeneration: fixture.assignment.Fence.Generation,
		PlanID: fixture.assignment.PlanID, Fence: fixture.assignment.Fence,
		ControlPlaneEndpoint: endpoint, LocalUpdateID: "33333333-3333-4333-8333-333333333333",
		Action: fixture.assignment.Action, Assignment: fixture.assignment,
	}
}

func TestRemoteFleetWorkerValidatesAssignmentSignaturePolicyAndDeadline(t *testing.T) {
	fixture := newFleetWorkerFixture(t)
	if err := fixture.worker.validateFleetRunnerAssignment(fixture.assignment); err != nil {
		t.Fatalf("valid assignment rejected: %v", err)
	}
	for name, mutate := range map[string]func(*model.FleetRunnerUpdateAssignment){
		"runner": func(value *model.FleetRunnerUpdateAssignment) { value.RunnerID = "runner-b" },
		"server": func(value *model.FleetRunnerUpdateAssignment) { value.ServerID = "server-b" },
		"deadline": func(value *model.FleetRunnerUpdateAssignment) {
			value.ExecutionDeadlineAt = time.Now().UTC().Add(-time.Second)
		},
		"policy": func(value *model.FleetRunnerUpdateAssignment) {
			value.PolicyDigest = "sha256:" + strings.Repeat("d", 64)
		},
		"signed manifest": func(value *model.FleetRunnerUpdateAssignment) {
			value.Manifest.ArtifactRevision = strings.Repeat("e", 40)
		},
	} {
		t.Run(name, func(t *testing.T) {
			assignment := fixture.assignment
			mutate(&assignment)
			if err := fixture.worker.validateFleetRunnerAssignment(assignment); err == nil {
				t.Fatal("tampered assignment was accepted")
			}
		})
	}
}

func TestRemoteFleetWorkerRejectsHeartbeatReceiptIdentityMismatch(t *testing.T) {
	fixture := newFleetWorkerFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var input RunnerHeartbeatRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Error(err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		if valid, err := VerifyHeartbeatPayload(fixture.publicKey, input); err != nil || !valid ||
			input.PayloadVersion != RunnerIdentityPayloadVersion ||
			request.Header.Get(runnerIDHeader) != fixture.worker.RunnerID {
			t.Errorf("signed heartbeat valid=%v input=%+v err=%v", valid, input, err)
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(model.RunnerNode{
			ID: fixture.worker.RunnerID, ServerID: "server-a", Version: "v2",
			Revision: strings.Repeat("f", 40), BinaryDigest: fixture.assignment.Manifest.ArtifactDigest,
			IdentityPayloadVersion: RunnerIdentityPayloadVersion, LeaseGeneration: 1, State: model.NodeOnline,
		})
	}))
	defer server.Close()
	fixture.worker.Endpoint, fixture.worker.Client = server.URL, server.Client()
	fixture.worker.Identity = func() (string, string, string, error) {
		return "v2", fixture.assignment.Manifest.ArtifactRevision, fixture.assignment.Manifest.ArtifactDigest, nil
	}
	if err := fixture.worker.sendIdentityHeartbeat(context.Background()); err == nil || !strings.Contains(err.Error(), "回执不一致") {
		t.Fatalf("mismatched heartbeat receipt err=%v", err)
	}
}

func TestRemoteFleetWorkerArtifactDownloadChecksHeaderLengthAndDigest(t *testing.T) {
	tests := map[string]struct {
		headerDigest string
		body         []byte
		lengthDelta  int
		wantError    bool
		wantRetry    bool
	}{
		"valid":           {body: []byte("signed-fleet-runner-v2")},
		"header mismatch": {headerDigest: "sha256:" + strings.Repeat("0", 64), body: []byte("signed-fleet-runner-v2"), wantError: true},
		"length mismatch": {body: []byte("signed-fleet-runner-v2"), lengthDelta: 1, wantError: true, wantRetry: true},
		"digest mismatch": {body: []byte("tampered-fleet-runner"), wantError: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newFleetWorkerFixture(t)
			headerDigest := test.headerDigest
			if headerDigest == "" {
				headerDigest = fixture.assignment.Manifest.ArtifactDigest
			}
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("X-AreaSong-Artifact-Digest", headerDigest)
				response.Header().Set("Content-Length", strconv.Itoa(len(test.body)+test.lengthDelta))
				_, _ = response.Write(test.body)
			}))
			defer server.Close()
			fixture.worker.Endpoint, fixture.worker.Client = server.URL, server.Client()
			path, err := fixture.worker.downloadFleetRunnerArtifact(context.Background(), fixture.receipt(server.URL))
			if test.wantError {
				if err == nil {
					_ = os.Remove(path)
					t.Fatal("invalid artifact response was accepted")
				}
				if isRetryableRemoteWorkerError(err) != test.wantRetry {
					t.Fatalf("retryable=%v err=%v", isRetryableRemoteWorkerError(err), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(path)
			content, err := os.ReadFile(path)
			if err != nil || string(content) != string(fixture.content) {
				t.Fatalf("downloaded=%q err=%v", content, err)
			}
		})
	}
}

func TestRemoteFleetWorkerResumesLaunchingReceiptAfterRestart(t *testing.T) {
	fixture := newFleetWorkerFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	fixture.worker.Endpoint, fixture.worker.Client = server.URL, server.Client()
	receipt := fixture.receipt(server.URL)
	if _, fresh, err := fixture.db.SaveFleetRunnerUpdateReceipt(context.Background(), receipt); err != nil || !fresh {
		t.Fatalf("save receipt fresh=%v err=%v", fresh, err)
	}
	if err := fixture.db.UpdateFleetRunnerUpdateReceipt(context.Background(), receipt.ItemID, receipt.AssignmentGeneration, "launching", receipt.LocalUpdateID, ""); err != nil {
		t.Fatal(err)
	}
	update := model.RunnerUpdate{
		ID: receipt.LocalUpdateID, IdempotencyKey: "44444444-4444-4444-8444-444444444444",
		RequestDigest: "sha256:" + strings.Repeat("1", 64), RunnerID: fixture.worker.RunnerID,
		TargetVersion: "v2", ArtifactDigest: fixture.assignment.Manifest.ArtifactDigest,
		ArtifactRevision: fixture.assignment.Manifest.ArtifactRevision, State: "prepared", Phase: "prepared",
		PreviousVersion: "v1", PreviousRevision: fixture.assignment.PreviousRevision,
		PreviousDigest: fixture.assignment.PreviousDigest, ConfirmationPhrase: "activate fleet update",
		CreatedAt: time.Now().UTC(),
	}
	if _, fresh, err := fixture.db.ReserveRunnerUpdate(context.Background(), update, "prepare-actor"); err != nil || !fresh {
		t.Fatalf("reserve local update fresh=%v err=%v", fresh, err)
	}
	if _, fresh, err := fixture.db.BeginRunnerUpdateActivation(context.Background(), update.ID, "execute-actor", "55555555-5555-4555-8555-555555555555", update.ConfirmationPhrase); err != nil || !fresh {
		t.Fatalf("activate local update fresh=%v err=%v", fresh, err)
	}
	launcher := &fakeRunnerUpdateLauncher{}
	fixture.worker.RunnerUpdater = launcher
	stored, found, err := fixture.db.GetFleetRunnerUpdateReceipt(context.Background(), receipt.ItemID, receipt.AssignmentGeneration)
	if err != nil || !found {
		t.Fatalf("stored receipt found=%v err=%v", found, err)
	}
	if err := fixture.worker.processFleetRunnerUpdateReceipt(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	stored, found, err = fixture.db.GetFleetRunnerUpdateReceipt(context.Background(), receipt.ItemID, receipt.AssignmentGeneration)
	if err != nil || !found || stored.State != "launched" || launcher.calls != 1 {
		t.Fatalf("resumed receipt=%+v launcher_calls=%d found=%v err=%v", stored, launcher.calls, found, err)
	}
}

func TestRemoteFleetWorkerTreatsTerminalCompletionAsReported(t *testing.T) {
	fixture := newFleetWorkerFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "assignment closed", http.StatusPreconditionFailed)
	}))
	defer server.Close()
	fixture.worker.Endpoint, fixture.worker.Client = server.URL, server.Client()
	receipt := fixture.receipt(server.URL)
	if _, _, err := fixture.db.SaveFleetRunnerUpdateReceipt(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.UpdateFleetRunnerUpdateReceipt(context.Background(), receipt.ItemID, receipt.AssignmentGeneration, "needs_attention", receipt.LocalUpdateID, "local failure"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.worker.reportFleetRunnerNeedsAttention(context.Background(), receipt, "local failure"); err != nil {
		t.Fatal(err)
	}
	stored, found, err := fixture.db.GetFleetRunnerUpdateReceipt(context.Background(), receipt.ItemID, receipt.AssignmentGeneration)
	if err != nil || !found || stored.State != "reported" {
		t.Fatalf("reported receipt=%+v found=%v err=%v", stored, found, err)
	}
}

func TestRemoteFleetWorkerKeepsReceiptPendingOnTemporaryArtifactFailure(t *testing.T) {
	fixture := newFleetWorkerFixture(t)
	fixture.worker.Identity = func() (string, string, string, error) {
		return fixture.assignment.PreviousVersion, fixture.assignment.PreviousRevision,
			fixture.assignment.PreviousDigest, nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/heartbeat") {
			response.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(response, "temporary outage", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	fixture.worker.Endpoint, fixture.worker.Client = server.URL, server.Client()
	receipt := fixture.receipt(server.URL)
	stored, _, err := fixture.db.SaveFleetRunnerUpdateReceipt(context.Background(), receipt)
	if err != nil {
		t.Fatal(err)
	}
	err = fixture.worker.processFleetRunnerUpdateReceipt(context.Background(), stored)
	if !isRetryableRemoteWorkerError(err) {
		t.Fatalf("temporary artifact failure was not retryable: %v", err)
	}
	stored, found, err := fixture.db.GetFleetRunnerUpdateReceipt(
		context.Background(), receipt.ItemID, receipt.AssignmentGeneration,
	)
	if err != nil || !found || stored.State == "needs_attention" || stored.State == "reported" {
		t.Fatalf("temporary failure changed terminal receipt state: %+v found=%v err=%v", stored, found, err)
	}
}
