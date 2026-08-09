package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCatalogRejectsEnabledAllowlistWithoutTargets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "services.json")
	content := `{
  "schemaVersion": 1,
  "services": {
    "demo": {
      "name": "demo",
      "displayName": "Demo",
      "description": "test",
      "adapter": "/tmp/demo-adapter",
      "actions": {
        "update": {
          "name": "update",
          "displayName": "更新",
          "enabled": true,
          "risk": "high",
          "targetMode": "allowlist",
          "steps": ["preflight", "apply"],
          "timeoutSeconds": 60,
          "confirmationTemplate": "更新 {service} 到 {target}",
          "impact": "test",
          "rollback": "test",
          "scope": "test"
        }
      }
    }
  }
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, false); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestProductionExampleCatalogIsValid(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "config", "services.example.json")
	catalog, err := Load(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Services) != 2 {
		t.Fatalf("services=%d", len(catalog.Services))
	}
}

func TestCatalogAcceptsReadOnlyAction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "services.json")
	content := `{
  "schemaVersion": 1,
  "services": {
    "demo": {
      "name": "demo",
      "displayName": "Demo",
      "description": "test",
      "adapter": "/tmp/demo-adapter",
      "actions": {
        "inspect": {
          "name": "inspect",
          "displayName": "检查",
          "enabled": true,
          "risk": "read_only",
          "targetMode": "none",
          "steps": ["inspect"],
          "timeoutSeconds": 30,
          "impact": "无变更",
          "rollback": "无需回滚",
          "scope": "单服务"
        }
      }
    }
  }
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, false); err != nil {
		t.Fatal(err)
	}
}
