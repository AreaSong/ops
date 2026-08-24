package runner

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCanonicalHeartbeatPayloadIsVersionedAndDeterministic(t *testing.T) {
	input := RunnerHeartbeatRequest{
		RunnerID: "runner-a", Version: "v1", Timestamp: "2026-08-21T00:00:00+08:00", Nonce: "nonce-123456",
		Capabilities: []string{"z", "a"}, Labels: map[string]string{"role": "prod", "env": "production"},
	}
	first, err := CanonicalHeartbeatPayload(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Capabilities = []string{"a", "z"}
	second, err := CanonicalHeartbeatPayload(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("canonical payload changed with capability order: %s vs %s", first, second)
	}
	var decoded map[string]any
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["payloadVersion"] != float64(HeartbeatPayloadVersion) {
		t.Fatalf("payload version=%v", decoded["payloadVersion"])
	}
	if !strings.Contains(string(first), `"timestamp":"2026-08-20T16:00:00Z"`) {
		t.Fatalf("timestamp was not normalized: %s", first)
	}
}

func TestHeartbeatEd25519SignatureAndDigestBindPayload(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	input := RunnerHeartbeatRequest{
		RunnerID: "runner-a", Version: "v1", Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Nonce: "nonce-123456",
	}
	signature, err := SignHeartbeatPayload(privateKey, input)
	if err != nil {
		t.Fatal(err)
	}
	input.Signature = signature
	valid, err := VerifyHeartbeatPayload(publicKey, input)
	if err != nil || !valid {
		t.Fatalf("signature valid=%v err=%v", valid, err)
	}
	digest, err := HeartbeatPayloadDigest(input)
	if err != nil || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	input.Version = "v2"
	valid, err = VerifyHeartbeatPayload(publicKey, input)
	if err != nil {
		t.Fatal(err)
	}
	if valid {
		t.Fatal("signature remained valid after signed payload mutation")
	}
}
