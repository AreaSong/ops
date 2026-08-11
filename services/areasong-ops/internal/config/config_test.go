package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func validComposeCatalog() *Catalog {
	return &Catalog{SchemaVersion: 3, Services: map[string]model.ServiceDefinition{
		"demo": {
			Name: "demo", ObjectID: "service:demo", DisplayName: "Demo", Description: "test", Template: "compose-service-v1",
			Adapter: "/usr/local/libexec/demo.sh",
			Runtime: &model.ComposeServiceRuntime{
				ControlledCompose: "/opt/ops/demo/compose.yml", RuntimeCompose: "/opt/services/demo/compose.yml",
				EnvFile: "/opt/services/demo/.env", ApplicationService: "demo", ApplicationContainer: "demo",
				HealthURL: "http://127.0.0.1:8080/health", ReleaseRepository: "owner/demo",
				ReleaseCatalog: "/opt/ops/demo/releases.json", PreparedReleaseDir: "/var/lib/areasong-ops/prepared/demo",
				InspectExecutable: "/usr/local/libexec/demo-inspect.sh",
			},
			Actions: map[string]model.ActionDefinition{
				"inspect": {Name: "inspect", DisplayName: "检查", Enabled: true, Risk: model.RiskReadOnly,
					TargetMode: "none", Steps: []string{"inspect"}, TimeoutSeconds: 30,
					Impact: "无变更", Rollback: "无需回滚", Scope: "单服务"},
			},
		},
	}}
}

func TestCatalogRejectsEnabledAllowlistWithoutTargets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "services.json")
	content := `{
  "schemaVersion": 3,
  "services": {
    "demo": {
      "name": "demo",
      "objectId": "service:demo",
      "displayName": "Demo",
      "description": "test",
      "template": "custom",
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
	if len(catalog.AutomaticTasks) != 2 || len(catalog.Adapters) != 3 {
		t.Fatalf("automaticTasks=%d adapters=%d", len(catalog.AutomaticTasks), len(catalog.Adapters))
	}
}

func TestCatalogRejectsUntrustedAutomaticTaskAdapter(t *testing.T) {
	catalog := &Catalog{
		SchemaVersion: 4,
		Adapters: map[string]model.AdapterDefinition{
			"service-v1": {Path: "/usr/local/libexec/service", AllowedTypes: []string{"service"}},
		},
		Services: validComposeCatalog().Services,
		AutomaticTasks: map[string]model.ServiceDefinition{
			"collector": {
				Name: "collector", ObjectID: "automatic-task:collector", DisplayName: "Collector",
				Template: "automatic-task-v1", AdapterRef: "service-v1",
				Metadata: model.ObjectMetadata{Type: "automatic_task", Environment: "production", Owner: "operations",
					Criticality: "important", Lifecycle: "active", Maturity: "manual_approval"},
				AutomaticTask: &model.AutomaticTaskRuntime{Schedule: "每分钟", ScheduleSource: "cron", FreshnessSeconds: 180},
				Actions: map[string]model.ActionDefinition{"inspect": {
					Name: "inspect", DisplayName: "检查", Enabled: true, Risk: model.RiskReadOnly,
					TargetMode: "none", Steps: []string{"inspect"}, TimeoutSeconds: 30,
					Impact: "无变更", Rollback: "无需回滚", Scope: "单任务",
				}},
			},
		},
	}
	service := catalog.Services["demo"]
	service.Adapter = ""
	service.AdapterRef = "service-v1"
	service.Metadata = model.ObjectMetadata{Type: "service", Environment: "production", Owner: "operations",
		Criticality: "important", Lifecycle: "active", Maturity: "manual_approval"}
	catalog.Services["demo"] = service
	if err := catalog.Validate(false); err == nil {
		t.Fatal("expected adapter type mismatch")
	}
}

func TestSchemaFourRejectsDirectAdapterPath(t *testing.T) {
	catalog := validComposeCatalog()
	catalog.SchemaVersion = 4
	catalog.Adapters = map[string]model.AdapterDefinition{
		"service-v1": {Path: "/usr/local/libexec/service", AllowedTypes: []string{"service"}},
	}
	service := catalog.Services["demo"]
	service.Metadata = model.ObjectMetadata{Type: "service", Environment: "production", Owner: "operations",
		Criticality: "important", Lifecycle: "active", Maturity: "manual_approval"}
	catalog.Services["demo"] = service
	if err := catalog.Validate(false); err == nil {
		t.Fatal("expected schema 4 direct adapter path to be rejected")
	}
}

func TestProposedObjectCanOnlyExposeReadOnlyActions(t *testing.T) {
	catalog := validComposeCatalog()
	service := catalog.Services["demo"]
	service.Metadata = model.ObjectMetadata{Type: "service", Environment: "production", Owner: "operations",
		Criticality: "important", Lifecycle: "proposed", Maturity: "inspect_only"}
	service.Actions["restart"] = model.ActionDefinition{
		Name: "restart", DisplayName: "重启", Enabled: true, Risk: model.RiskMedium,
		TargetMode: "none", Steps: []string{"restart"}, TimeoutSeconds: 60, ObservationSeconds: 300,
		ConfirmationTemplate: "重启 {service}", Impact: "短暂中断", Rollback: "重新启动", Scope: "单服务",
	}
	service.AlertPolicy = model.AlertPolicyDefinition{
		Matchers: map[string]string{"service": "demo"}, BlockingAlerts: []string{"AppHttpProbeFailed"},
		MaintenanceAlerts: []string{"AppHttpProbeFailed"},
	}
	catalog.Services["demo"] = service
	if err := catalog.Validate(false); err == nil {
		t.Fatal("expected proposed inspect-only object mutation to be rejected")
	}
}

func TestRetiredObjectCannotExposeActions(t *testing.T) {
	catalog := validComposeCatalog()
	service := catalog.Services["demo"]
	service.Metadata = model.ObjectMetadata{Type: "service", Environment: "production", Owner: "operations",
		Criticality: "important", Lifecycle: "retired", Maturity: "manual_approval"}
	catalog.Services["demo"] = service
	if err := catalog.Validate(false); err == nil {
		t.Fatal("expected retired object actions to be rejected")
	}
}

func TestCatalogAcceptsReadOnlyAction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "services.json")
	content := `{
  "schemaVersion": 3,
  "services": {
    "demo": {
      "name": "demo",
      "objectId": "service:demo",
      "displayName": "Demo",
      "description": "test",
      "template": "custom",
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

func TestCatalogRequiresBoundedObservationForProductionMutation(t *testing.T) {
	catalog := validComposeCatalog()
	service := catalog.Services["demo"]
	service.Actions["restart"] = model.ActionDefinition{
		Name: "restart", DisplayName: "重启", Enabled: true, Risk: model.RiskMedium,
		TargetMode: "none", Steps: []string{"preflight", "restart", "health"}, TimeoutSeconds: 60,
		ConfirmationTemplate: "重启 {service}", Impact: "短暂中断", Rollback: "重新启动", Scope: "单服务",
	}
	catalog.Services["demo"] = service
	if err := catalog.Validate(false); err == nil {
		t.Fatal("expected missing observation window to be rejected")
	}
	action := service.Actions["restart"]
	action.ObservationSeconds = 300
	service.Actions["restart"] = action
	service.AlertPolicy = model.AlertPolicyDefinition{
		Matchers:          map[string]string{"service": "demo"},
		BlockingAlerts:    []string{"AppHttpProbeFailed"},
		MaintenanceAlerts: []string{"AppHttpProbeFailed"},
	}
	catalog.Services["demo"] = service
	if err := catalog.Validate(false); err != nil {
		t.Fatalf("valid observation window rejected: %v", err)
	}
}

func TestCatalogRejectsUnsupportedSchema(t *testing.T) {
	catalog := validComposeCatalog()
	catalog.SchemaVersion = 1
	if err := catalog.Validate(false); err == nil {
		t.Fatal("expected schema validation error")
	}
}

func TestCatalogRejectsInvalidAlertPolicy(t *testing.T) {
	newCatalog := func() *Catalog {
		catalog := validComposeCatalog()
		service := catalog.Services["demo"]
		service.Actions["restart"] = model.ActionDefinition{
			Name: "restart", DisplayName: "重启", Enabled: true, Risk: model.RiskMedium,
			TargetMode: "none", Steps: []string{"restart"}, ObservationSeconds: 300,
			TimeoutSeconds: 60, ConfirmationTemplate: "重启 {service}",
			Impact: "短暂中断", Rollback: "重新启动", Scope: "单服务",
		}
		catalog.Services["demo"] = service
		return catalog
	}

	t.Run("matcher 必须精确匹配服务", func(t *testing.T) {
		catalog := newCatalog()
		service := catalog.Services["demo"]
		service.AlertPolicy = model.AlertPolicyDefinition{
			Matchers:          map[string]string{"service": "other"},
			BlockingAlerts:    []string{"AppHttpProbeFailed"},
			MaintenanceAlerts: []string{"AppHttpProbeFailed"},
		}
		catalog.Services["demo"] = service
		if err := catalog.Validate(false); err == nil {
			t.Fatal("expected mismatched service matcher to be rejected")
		}
	})

	t.Run("维护静默必须属于阻断映射", func(t *testing.T) {
		catalog := newCatalog()
		service := catalog.Services["demo"]
		service.AlertPolicy = model.AlertPolicyDefinition{
			Matchers:          map[string]string{"service": "demo"},
			BlockingAlerts:    []string{"AppHttpProbeFailed"},
			MaintenanceAlerts: []string{"BusinessHttpProbeFailed"},
		}
		catalog.Services["demo"] = service
		if err := catalog.Validate(false); err == nil {
			t.Fatal("expected unmapped maintenance alert to be rejected")
		}
	})
}

func TestCatalogRejectsComposeTemplateWithoutRuntime(t *testing.T) {
	catalog := validComposeCatalog()
	service := catalog.Services["demo"]
	service.Runtime = nil
	catalog.Services["demo"] = service
	if err := catalog.Validate(false); err == nil {
		t.Fatal("expected runtime validation error")
	}
}

func TestCatalogRejectsRemoteHealthURL(t *testing.T) {
	catalog := validComposeCatalog()
	catalog.Services["demo"].Runtime.HealthURL = "https://example.com/health"
	if err := catalog.Validate(false); err == nil {
		t.Fatal("expected health URL validation error")
	}
}

func TestCatalogRejectsDisabledActionWithoutReason(t *testing.T) {
	catalog := validComposeCatalog()
	service := catalog.Services["demo"]
	action := service.Actions["inspect"]
	action.Enabled = false
	service.Actions["inspect"] = action
	catalog.Services["demo"] = service
	if err := catalog.Validate(false); err == nil {
		t.Fatal("expected disabled reason validation error")
	}
}

func TestSecureExecutableRejectsWritableHook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o775); err != nil {
		t.Fatal(err)
	}
	if err := verifySecureExecutable(path); err == nil {
		t.Fatal("expected insecure hook validation error")
	}
}
