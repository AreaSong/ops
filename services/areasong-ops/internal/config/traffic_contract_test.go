package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSchemaFourTrafficPolicyRejectsUnknownDriverField(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	examplePath := filepath.Join(filepath.Dir(currentFile), "..", "..", "config", "services.example.json")
	data, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatal(err)
	}
	needle := `"trafficPolicy": {
        "adapterPath":`
	replacement := `"trafficPolicy": {
        "driver": "nginx",
        "adapterPath":`
	mutated := strings.Replace(string(data), needle, replacement, 1)
	if mutated == string(data) {
		t.Fatal("failed to inject unknown traffic policy field")
	}
	path := filepath.Join(t.TempDir(), "services.json")
	if err := os.WriteFile(path, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, false); err == nil || !strings.Contains(err.Error(), "driver") {
		t.Fatalf("Load error=%v, want unknown driver field rejection", err)
	}
}
