package model_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestTenantLifecycleEncoding(t *testing.T) {
	request := model.TenantLifecycleTransitionRequest{Kind: model.TenantLifecycleKind, TenantID: "team", ExpectedStatus: "active", ExpectedGeneration: math.MaxInt64,
		TargetStatus: "disabled", ExpectedVersion: 7, RequiresDualApproval: true, IdempotencyKey: "key"}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := model.DecodeTenantLifecycleTransition(string(raw))
	if err != nil || decoded != request {
		t.Fatalf("%+v %v", decoded, err)
	}
	for name, payload := range map[string]string{
		"overflow":  strings.Replace(string(raw), "9223372036854775807", "9223372036854775808", 1),
		"fraction":  strings.Replace(string(raw), "9223372036854775807", "1.5", 1),
		"unknown":   strings.TrimSuffix(string(raw), "}") + `,"unknown":true}`,
		"duplicate": strings.Replace(string(raw), `"kind":`, `"kind":"other","kind":`, 1),
		"mixed":     strings.TrimSuffix(string(raw), "}") + `,"tenants":[]}`,
		"negative":  strings.Replace(string(raw), "9223372036854775807", "-1", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := model.DecodeTenantLifecycleTransition(payload); err == nil {
				t.Fatal("accepted", payload)
			}
		})
	}
	for _, payload := range []string{string(raw), `{"kind":"tenant_lifecycle_v1","kind":"other","idempotencyKey":"key"}`, `{"expectedGeneration":1,"tenants":[]}`, `{"TargetStatus":"disabled"}`, `{"tenantId":"team"}`, `{"tenants":[{"id":"team","lifecycleGeneration":1}]}`} {
		if err := model.RejectTenantLifecyclePayload(payload); err == nil {
			t.Fatal("generic accepted", payload)
		}
	}
}

func TestTenantLifecycleKeepsLegacyEncoding(t *testing.T) {
	tenant := `{"id":"team-a","displayName":"Team A","status":"active","createdAt":"0001-01-01T00:00:00Z","updatedAt":"0001-01-01T00:00:00Z","createdBy":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	for name, fixture := range map[string]struct {
		raw    string
		target any
	}{
		"tenant":   {tenant, &model.Tenant{}},
		"request":  {`{"tenants":[` + tenant + `],"requiresDualApproval":true,"expectedVersion":7,"idempotencyKey":"key"}`, &model.AccessControlUpdateRequest{}},
		"snapshot": {`{"enforced":true,"defaultTenant":"default","tenants":{"team-a":` + tenant + `}}`, &config.AccessPolicy{}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(fixture.raw), fixture.target); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(fixture.target)
			if err != nil || string(raw) != fixture.raw {
				t.Fatalf("%s %v", raw, err)
			}
		})
	}
}
