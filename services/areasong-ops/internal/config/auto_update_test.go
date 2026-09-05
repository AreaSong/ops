package config

import (
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestValidateAutoUpdatePolicyTimezone(t *testing.T) {
	policy := &model.AutoUpdatePolicy{Channel: "stable", MaintenanceWindow: "02:00-04:00"}
	if err := validateAutoUpdatePolicy("demo", policy); err != nil {
		t.Fatal(err)
	}
	if policy.MaintenanceTimezone != "UTC" {
		t.Fatalf("default timezone=%q want UTC", policy.MaintenanceTimezone)
	}
	policy.MaintenanceTimezone = "Asia/Shanghai"
	if err := validateAutoUpdatePolicy("demo", policy); err != nil {
		t.Fatalf("IANA timezone rejected: %v", err)
	}
	policy.MaintenanceTimezone = "Not/AZone"
	if err := validateAutoUpdatePolicy("demo", policy); err == nil {
		t.Fatal("invalid timezone accepted")
	}
}
