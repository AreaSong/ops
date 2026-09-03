package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestMTLSListenerRejectsIncompletePolicy(t *testing.T) {
	if _, err := mtlsListener(&config.FleetPolicy{AllowRemoteRunners: true, RequiremTLS: true}); err == nil {
		t.Fatal("incomplete mTLS policy was accepted")
	}
}

func TestRemoteWorkerHeartbeatKeyMustMatchInventory(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "heartbeat.key")
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(privateKey)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := &config.Catalog{Fleet: &config.FleetPolicy{
		RunnerPublicKeys: map[string]string{"runner-test": base64.StdEncoding.EncodeToString(publicKey)},
	}}
	policy := &config.RemoteWorkerPolicy{RunnerID: "runner-test", HeartbeatPrivateKeyFile: path}
	loaded, err := loadRemoteWorkerHeartbeatKey(catalog, policy)
	if err != nil || !loaded.Equal(privateKey) {
		t.Fatalf("loaded=%v err=%v", loaded != nil, err)
	}
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	catalog.Fleet.RunnerPublicKeys["runner-test"] = base64.StdEncoding.EncodeToString(otherPublic)
	if _, err := loadRemoteWorkerHeartbeatKey(catalog, policy); err == nil {
		t.Fatal("mismatched heartbeat private key was accepted")
	}
}

func TestRemoteWorkerCertificateMustDeclareRunnerID(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "runner-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: privateKey}
	if err := verifyRemoteWorkerCertificateIdentity(certificate, "runner-test"); err != nil {
		t.Fatal(err)
	}
	if err := verifyRemoteWorkerCertificateIdentity(certificate, "runner-other"); err == nil {
		t.Fatal("certificate for another runner was accepted")
	}
}

func TestEnforceProductionStateRootMode(t *testing.T) {
	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := enforceProductionStateRootMode(stateRoot); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o710 {
		t.Fatalf("state root mode = %04o, want 0710", got)
	}
}

func TestInterruptionClassifierUsesPhaseSemantics(t *testing.T) {
	action := model.ActionDefinition{
		Name: "deploy", Steps: []string{"preflight", "apply", "health"},
		PhaseSemantics: map[string]model.PhaseSemantics{
			"apply": {
				Effect: "runtime_mutation", FailurePolicy: "rollback", RecoveryPhase: "undo",
			},
		},
	}
	catalog := &config.Catalog{Services: map[string]model.ServiceDefinition{
		"demo": {Name: "demo", Actions: map[string]model.ActionDefinition{"deploy": action}},
	}}
	classify := interruptionClassifier(catalog)
	mutation, rollback := classify("demo", "deploy", "apply", false)
	if !mutation || !rollback {
		t.Fatalf("apply mutation=%v rollback=%v", mutation, rollback)
	}
	mutation, rollback = classify("demo", "deploy", "health", true)
	if mutation || !rollback {
		t.Fatalf("legacy health mutation=%v rollback=%v", mutation, rollback)
	}
	action.PhaseSemantics["health"] = model.PhaseSemantics{Effect: "observe", FailurePolicy: "fail"}
	service := catalog.Services["demo"]
	service.Actions["deploy"] = action
	catalog.Services["demo"] = service
	mutation, rollback = classify("demo", "deploy", "health", true)
	if mutation || rollback {
		t.Fatalf("explicit health mutation=%v rollback=%v", mutation, rollback)
	}
	mutation, rollback = classify("unknown", "deploy", "health", false)
	if !mutation || rollback {
		t.Fatalf("unknown mutation=%v rollback=%v", mutation, rollback)
	}
}
