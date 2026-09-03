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

func TestRunnerIdentityHeartbeatV2BindsRevisionAndBinaryDigest(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	input := RunnerHeartbeatRequest{
		PayloadVersion: RunnerIdentityPayloadVersion,
		RunnerID:       "runner-a", Version: "v2", Revision: strings.Repeat("a", 40),
		BinaryDigest: "sha256:" + strings.Repeat("b", 64),
		Timestamp:    time.Now().UTC().Format(time.RFC3339Nano), Nonce: "identity-nonce-123",
		Capabilities: []string{"runner-update"}, Labels: map[string]string{"tenant": "a"},
	}
	input.Signature, err = SignHeartbeatPayload(privateKey, input)
	if err != nil {
		t.Fatal(err)
	}
	if valid, verifyErr := VerifyHeartbeatPayload(publicKey, input); verifyErr != nil || !valid {
		t.Fatalf("v2 identity signature valid=%v err=%v", valid, verifyErr)
	}
	for name, mutate := range map[string]func(*RunnerHeartbeatRequest){
		"revision": func(value *RunnerHeartbeatRequest) { value.Revision = strings.Repeat("c", 40) },
		"digest": func(value *RunnerHeartbeatRequest) {
			value.BinaryDigest = "sha256:" + strings.Repeat("d", 64)
		},
		"capability": func(value *RunnerHeartbeatRequest) { value.Capabilities = []string{"backup"} },
		"label":      func(value *RunnerHeartbeatRequest) { value.Labels = map[string]string{"tenant": "b"} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := input
			mutate(&changed)
			if valid, verifyErr := VerifyHeartbeatPayload(publicKey, changed); verifyErr != nil || valid {
				t.Fatalf("mutated v2 identity valid=%v err=%v", valid, verifyErr)
			}
		})
	}
}
