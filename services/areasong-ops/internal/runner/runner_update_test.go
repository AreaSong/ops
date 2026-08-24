package runner

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/buildinfo"
	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

type fakeRunnerUpdateLauncher struct {
	calls int
	err   error
}

func (launcher *fakeRunnerUpdateLauncher) Launch(
	context.Context,
	config.RunnerUpdatePolicy,
	model.RunnerUpdate,
) error {
	launcher.calls++
	return launcher.err
}

type runnerUpdateFixture struct {
	engine     *Engine
	database   *store.Store
	launcher   *fakeRunnerUpdateLauncher
	request    model.RunnerUpdateRequest
	artifact   string
	privateKey ed25519.PrivateKey
	prepareBy  string
	activateBy string
}

func newRunnerUpdateFixture(t *testing.T) runnerUpdateFixture {
	t.Helper()
	root := t.TempDir()
	database, err := store.Open(filepath.Join(root, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	incoming := filepath.Join(root, "incoming")
	if err := os.MkdirAll(incoming, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(incoming, "runner-v2")
	if err := os.WriteFile(artifact, []byte("signed-runner-v2"), 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "current-runner")
	if err := os.WriteFile(binary, []byte("runner-v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	policy := &config.RunnerUpdatePolicy{
		Enabled: true, RunnerID: "runner-test", ArtifactRoot: incoming,
		BinaryPath: binary, UnitName: "areasong-ops-runner.service",
		UpdaterUnitName: "areasong-ops-runner-update@.service", Publisher: "release",
		ManifestPurpose:      config.RunnerUpdateManifestPurpose,
		ManifestSchema:       config.RunnerUpdateManifestSchema,
		ManifestGOOS:         config.RunnerUpdateManifestGOOS,
		ManifestGOARCH:       config.RunnerUpdateManifestGOARCH,
		TrustedPublisherKeys: map[string]string{"release": base64.StdEncoding.EncodeToString(publicKey)},
		HealthTimeoutSeconds: 30, MaxArtifactBytes: 1 << 20,
	}
	launcher := &fakeRunnerUpdateLauncher{}
	catalog := &config.Catalog{
		SchemaVersion: 3, Services: map[string]model.ServiceDefinition{}, RunnerUpdate: policy,
	}
	engine, err := NewEngineChecked(catalog, database, &fakeExecutor{}, root, WithRunnerUpdateLauncher(launcher))
	if err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("a", 40)
	request := model.RunnerUpdateRequest{
		Manifest: model.RunnerUpdateManifest{
			Purpose: config.RunnerUpdateManifestPurpose, Schema: config.RunnerUpdateManifestSchema,
			GOOS: config.RunnerUpdateManifestGOOS, GOARCH: config.RunnerUpdateManifestGOARCH,
			RunnerID: policy.RunnerID, TargetVersion: "v2",
			ArtifactDigest: digestText("signed-runner-v2"), ArtifactRevision: strings.Repeat("a", 40), Publisher: "release",
		},
		ManifestPurpose: config.RunnerUpdateManifestPurpose, ManifestSchema: config.RunnerUpdateManifestSchema,
		ManifestGOOS: config.RunnerUpdateManifestGOOS, ManifestGOARCH: config.RunnerUpdateManifestGOARCH,
		RunnerID: policy.RunnerID, TargetVersion: "v2", ArtifactPath: "runner-v2",
		ArtifactDigest: digestText("signed-runner-v2"), ArtifactRevision: revision,
		Publisher: "release", Confirmation: "准备 Runner 更新到 v2",
		IdempotencyKey: "11111111-1111-4111-8111-111111111111",
	}
	payload, err := runnerUpdateManifestPayload(policy, request)
	if err != nil {
		t.Fatal(err)
	}
	request.ArtifactSignature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	return runnerUpdateFixture{
		engine: engine, database: database, launcher: launcher, request: request, artifact: artifact,
		privateKey: privateKey,
		prepareBy:  strings.Repeat("1", 64), activateBy: strings.Repeat("2", 64),
	}
}

func signRunnerUpdateRequest(t *testing.T, fixture runnerUpdateFixture, request *model.RunnerUpdateRequest) {
	t.Helper()
	payload, err := runnerUpdateManifestPayload(fixture.engine.catalog.RunnerUpdate, *request)
	if err != nil {
		t.Fatal(err)
	}
	request.ArtifactSignature = base64.StdEncoding.EncodeToString(ed25519.Sign(fixture.privateKey, payload))
}

func TestRunnerUpdatePrepareStagesSignedArtifactAndReplaysWithoutSource(t *testing.T) {
	fixture := newRunnerUpdateFixture(t)
	originalVersion, originalRevision := buildinfo.Version, buildinfo.Revision
	buildinfo.Version, buildinfo.Revision = "v1", strings.Repeat("b", 40)
	t.Cleanup(func() { buildinfo.Version, buildinfo.Revision = originalVersion, originalRevision })

	prepared, created, err := fixture.engine.PrepareRunnerUpdate(
		context.Background(), fixture.prepareBy, fixture.request,
	)
	if err != nil || !created {
		t.Fatalf("prepared=%+v created=%v err=%v", prepared, created, err)
	}
	if prepared.StagedPath == "" || prepared.ArtifactRevision != fixture.request.ArtifactRevision ||
		prepared.PreparedByHash != fixture.prepareBy || prepared.ConfirmationPhrase == "" {
		t.Fatalf("prepared=%+v", prepared)
	}
	stagedContent, err := os.ReadFile(prepared.StagedPath)
	if err != nil || string(stagedContent) != "signed-runner-v2" {
		t.Fatalf("staged=%q err=%v", stagedContent, err)
	}
	if err := os.Remove(fixture.artifact); err != nil {
		t.Fatal(err)
	}
	replayed, created, err := fixture.engine.PrepareRunnerUpdate(
		context.Background(), fixture.prepareBy, fixture.request,
	)
	if err != nil || created || replayed.ID != prepared.ID {
		t.Fatalf("replayed=%+v created=%v err=%v", replayed, created, err)
	}
}

func TestRunnerUpdateRejectsTamperedSignedFields(t *testing.T) {
	fixture := newRunnerUpdateFixture(t)
	fixture.request.ArtifactRevision = strings.Repeat("c", 40)
	if _, _, err := fixture.engine.PrepareRunnerUpdate(
		context.Background(), fixture.prepareBy, fixture.request,
	); err == nil || !strings.Contains(err.Error(), "签名") {
		t.Fatalf("tampered signature err=%v", err)
	}
}

func TestRunnerUpdateSignatureBindsCompleteManifest(t *testing.T) {
	fixture := newRunnerUpdateFixture(t)
	legacyPayload, err := json.Marshal(struct {
		RunnerID         string `json:"runnerId"`
		TargetVersion    string `json:"targetVersion"`
		ArtifactDigest   string `json:"artifactDigest"`
		ArtifactRevision string `json:"artifactRevision"`
		Publisher        string `json:"publisher"`
	}{
		RunnerID: fixture.request.RunnerID, TargetVersion: fixture.request.TargetVersion,
		ArtifactDigest: fixture.request.ArtifactDigest, ArtifactRevision: fixture.request.ArtifactRevision,
		Publisher: fixture.request.Publisher,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.request.ArtifactSignature = base64.StdEncoding.EncodeToString(
		ed25519.Sign(fixture.privateKey, legacyPayload),
	)
	if _, _, err := fixture.engine.PrepareRunnerUpdate(
		context.Background(), fixture.prepareBy, fixture.request,
	); err == nil || !strings.Contains(err.Error(), "签名") {
		t.Fatalf("legacy signing payload err=%v", err)
	}
}

func TestRunnerUpdateRejectsTamperedManifestPurpose(t *testing.T) {
	fixture := newRunnerUpdateFixture(t)
	fixture.request.Manifest.Purpose = "areasong-ops.extension"
	fixture.request.ManifestPurpose = fixture.request.Manifest.Purpose
	if _, _, err := fixture.engine.PrepareRunnerUpdate(
		context.Background(), fixture.prepareBy, fixture.request,
	); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("tampered purpose err=%v", err)
	}
}

func TestRunnerUpdateRejectsTamperedManifestPlatform(t *testing.T) {
	fixture := newRunnerUpdateFixture(t)
	fixture.request.Manifest.GOARCH = "arm64"
	fixture.request.ManifestGOARCH = fixture.request.Manifest.GOARCH
	if _, _, err := fixture.engine.PrepareRunnerUpdate(
		context.Background(), fixture.prepareBy, fixture.request,
	); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("tampered platform err=%v", err)
	}
}

func TestRunnerUpdateRejectsManifestFieldMismatch(t *testing.T) {
	fixture := newRunnerUpdateFixture(t)
	fixture.request.Manifest.TargetVersion = "v3"
	if _, _, err := fixture.engine.PrepareRunnerUpdate(
		context.Background(), fixture.prepareBy, fixture.request,
	); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("manifest field mismatch err=%v", err)
	}
}

func TestRunnerUpdateManifestChangesInvalidateIdempotentReplay(t *testing.T) {
	fixture := newRunnerUpdateFixture(t)
	if _, created, err := fixture.engine.PrepareRunnerUpdate(
		context.Background(), fixture.prepareBy, fixture.request,
	); err != nil || !created {
		t.Fatalf("initial prepare created=%v err=%v", created, err)
	}
	replayed := fixture.request
	replayed.TargetVersion = "v3"
	replayed.Manifest.TargetVersion = replayed.TargetVersion
	replayed.Confirmation = "准备 Runner 更新到 v3"
	signRunnerUpdateRequest(t, fixture, &replayed)
	if _, _, err := fixture.engine.PrepareRunnerUpdate(
		context.Background(), fixture.prepareBy, replayed,
	); !errors.Is(err, store.ErrIdempotency) {
		t.Fatalf("manifest replay err=%v", err)
	}
}

func TestRunnerUpdateActivationAPIRequiresIndependentActorAndIsIdempotent(t *testing.T) {
	fixture := newRunnerUpdateFixture(t)
	prepared, created, err := fixture.engine.PrepareRunnerUpdate(
		context.Background(), fixture.prepareBy, fixture.request,
	)
	if err != nil || !created {
		t.Fatalf("prepare created=%v err=%v", created, err)
	}
	activation := model.RunnerUpdateActivationRequest{
		Confirmation:   prepared.ConfirmationPhrase,
		IdempotencyKey: "22222222-2222-4222-8222-222222222222",
	}
	if _, _, err := fixture.engine.ActivateRunnerUpdate(
		context.Background(), fixture.prepareBy, prepared.ID, activation,
	); err == nil || !strings.Contains(err.Error(), "必须不同") {
		t.Fatalf("same-actor activation err=%v", err)
	}
	body, _ := json.Marshal(activation)
	request := httptest.NewRequest(
		http.MethodPost, "/v1/runner/update/"+prepared.ID+"/activate", bytes.NewReader(body),
	)
	request.Header.Set(actorHeader, fixture.activateBy)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewServer(fixture.engine, fixture.database).ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("activation status=%d body=%s", response.Code, response.Body.String())
	}
	if fixture.launcher.calls != 1 {
		t.Fatalf("launcher calls=%d", fixture.launcher.calls)
	}
	replayed, created, err := fixture.engine.ActivateRunnerUpdate(
		context.Background(), fixture.activateBy, prepared.ID, activation,
	)
	if err != nil || created || replayed.State != "activating" || fixture.launcher.calls != 1 {
		t.Fatalf("replayed=%+v created=%v calls=%d err=%v", replayed, created, fixture.launcher.calls, err)
	}
}

func TestRunnerUpdateLauncherFailureNeedsAttention(t *testing.T) {
	fixture := newRunnerUpdateFixture(t)
	fixture.launcher.err = errors.New("systemd unavailable")
	prepared, _, err := fixture.engine.PrepareRunnerUpdate(
		context.Background(), fixture.prepareBy, fixture.request,
	)
	if err != nil {
		t.Fatal(err)
	}
	updated, created, err := fixture.engine.ActivateRunnerUpdate(
		context.Background(), fixture.activateBy, prepared.ID, model.RunnerUpdateActivationRequest{
			Confirmation:   prepared.ConfirmationPhrase,
			IdempotencyKey: "22222222-2222-4222-8222-222222222222",
		},
	)
	if err == nil || !created || updated.State != "needs_attention" {
		t.Fatalf("updated=%+v created=%v err=%v", updated, created, err)
	}
	stored, getErr := fixture.database.GetRunnerUpdate(context.Background(), prepared.ID)
	if getErr != nil || stored.State != "needs_attention" || stored.Phase != "launch_failed" {
		t.Fatalf("stored=%+v err=%v", stored, getErr)
	}
}

func TestRunnerUpdateCancelRemovesStagedArtifactAndUnblocksNextPrepare(t *testing.T) {
	fixture := newRunnerUpdateFixture(t)
	prepared, created, err := fixture.engine.PrepareRunnerUpdate(
		context.Background(), fixture.prepareBy, fixture.request,
	)
	if err != nil || !created {
		t.Fatalf("prepare created=%v err=%v", created, err)
	}
	cancellation := model.RunnerUpdateCancellationRequest{
		Confirmation:   "取消 Runner 更新 " + prepared.ID,
		IdempotencyKey: "44444444-4444-4444-8444-444444444444",
	}
	cancelled, created, err := fixture.engine.CancelRunnerUpdate(
		context.Background(), fixture.prepareBy, prepared.ID, cancellation,
	)
	if err != nil || !created || cancelled.State != "cancelled" || cancelled.Phase != "cancelled" {
		t.Fatalf("cancelled=%+v created=%v err=%v", cancelled, created, err)
	}
	if _, err := os.Stat(prepared.StagedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged artifact still exists: %v", err)
	}
	replayed, created, err := fixture.engine.CancelRunnerUpdate(
		context.Background(), fixture.prepareBy, prepared.ID, cancellation,
	)
	if err != nil || created || replayed.State != "cancelled" {
		t.Fatalf("cancel replay=%+v created=%v err=%v", replayed, created, err)
	}
	if _, _, err := fixture.engine.ActivateRunnerUpdate(
		context.Background(), fixture.activateBy, prepared.ID, model.RunnerUpdateActivationRequest{
			Confirmation:   prepared.ConfirmationPhrase,
			IdempotencyKey: "55555555-5555-4555-8555-555555555555",
		},
	); err == nil || !strings.Contains(err.Error(), "不能激活") {
		t.Fatalf("cancelled update activation err=%v", err)
	}
	nextRequest := fixture.request
	nextRequest.IdempotencyKey = "66666666-6666-4666-8666-666666666666"
	if _, created, err := fixture.engine.PrepareRunnerUpdate(
		context.Background(), fixture.prepareBy, nextRequest,
	); err != nil || !created {
		t.Fatalf("next prepare created=%v err=%v", created, err)
	}
}

func TestRunnerUpdateResolveNeedsAttentionIsIdempotent(t *testing.T) {
	fixture := newRunnerUpdateFixture(t)
	fixture.launcher.err = errors.New("systemd unavailable")
	prepared, _, err := fixture.engine.PrepareRunnerUpdate(
		context.Background(), fixture.prepareBy, fixture.request,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = fixture.engine.ActivateRunnerUpdate(
		context.Background(), fixture.activateBy, prepared.ID, model.RunnerUpdateActivationRequest{
			Confirmation:   prepared.ConfirmationPhrase,
			IdempotencyKey: "22222222-2222-4222-8222-222222222222",
		},
	)
	resolution := model.RunnerUpdateResolutionRequest{
		Confirmation:   "确认 Runner 更新已人工核对",
		IdempotencyKey: "77777777-7777-4777-8777-777777777777",
	}
	resolved, created, err := fixture.engine.ResolveRunnerUpdate(
		context.Background(), fixture.prepareBy, prepared.ID, resolution,
	)
	if err != nil || !created || resolved.State != "failed" || resolved.Phase != "manually_resolved" {
		t.Fatalf("resolved=%+v created=%v err=%v", resolved, created, err)
	}
	replayed, created, err := fixture.engine.ResolveRunnerUpdate(
		context.Background(), fixture.prepareBy, prepared.ID, resolution,
	)
	if err != nil || created || replayed.State != "failed" {
		t.Fatalf("resolve replay=%+v created=%v err=%v", replayed, created, err)
	}
}

func TestRunnerUpdateSchema4RejectsEmptyResolutionEvidence(t *testing.T) {
	fixture := newRunnerUpdateFixture(t)
	fixture.launcher.err = errors.New("systemd unavailable")
	prepared, _, err := fixture.engine.PrepareRunnerUpdate(
		context.Background(), fixture.prepareBy, fixture.request,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = fixture.engine.ActivateRunnerUpdate(
		context.Background(), fixture.activateBy, prepared.ID, model.RunnerUpdateActivationRequest{
			Confirmation: prepared.ConfirmationPhrase, IdempotencyKey: "22222222-2222-4222-8222-222222222222",
		},
	)
	fixture.engine.catalog.SchemaVersion = 4
	if _, _, err := fixture.engine.ResolveRunnerUpdate(
		context.Background(), strings.Repeat("3", 64), prepared.ID, model.RunnerUpdateResolutionRequest{
			Confirmation: "确认 Runner 更新已人工核对", IdempotencyKey: "77777777-7777-4777-8777-777777777777",
		},
	); err == nil || !strings.Contains(err.Error(), "现场核对证据") {
		t.Fatalf("empty schema-4 evidence err=%v", err)
	}
}

func TestRunnerUpdateActivationFailureResponseIncludesPersistedState(t *testing.T) {
	fixture := newRunnerUpdateFixture(t)
	fixture.launcher.err = errors.New("systemd unavailable")
	prepared, _, err := fixture.engine.PrepareRunnerUpdate(
		context.Background(), fixture.prepareBy, fixture.request,
	)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(model.RunnerUpdateActivationRequest{
		Confirmation:   prepared.ConfirmationPhrase,
		IdempotencyKey: "22222222-2222-4222-8222-222222222222",
	})
	request := httptest.NewRequest(
		http.MethodPost, "/v1/runner/update/"+prepared.ID+"/activate", bytes.NewReader(payload),
	)
	request.Header.Set(actorHeader, fixture.activateBy)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewServer(fixture.engine, fixture.database).ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Error  string             `json:"error"`
		Update model.RunnerUpdate `json:"update"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error == "" || body.Update.State != "needs_attention" || body.Update.Phase != "launch_failed" {
		t.Fatalf("body=%+v", body)
	}
	serialized := response.Body.String()
	for _, hidden := range []string{"stagedPath", "binaryPath", "unitName", "executorHeartbeatAt"} {
		if strings.Contains(serialized, hidden) {
			t.Fatalf("response leaked %s: %s", hidden, serialized)
		}
	}
}
