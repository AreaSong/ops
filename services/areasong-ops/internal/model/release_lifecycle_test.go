package model

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
)

func releaseBindingFixture() ReleaseLifecycleBinding {
	return ReleaseLifecycleBinding{Version: 1, Source: "manual_single", ExecutionMode: "local", PreparationID: "preparation", CreatorTenantID: "actor",
		ScopeDigest: strings.Repeat("a", 64), TargetObjects: []ReleaseTargetObject{{ObjectID: "service:demo", TenantID: "target", ServerID: "local"}},
		Targets: []ReleaseGeneration{{TenantID: "actor", ExpectedGeneration: "1"}, {TenantID: "target", ExpectedGeneration: strconv.FormatInt(math.MaxInt64, 10)}}}
}

func TestReleaseLifecycleV2EncodingAndLegacyDigest(t *testing.T) {
	legacy := ApprovalSummary{SchemaVersion: 1, Service: "demo", Action: "update", Target: "v1.1.0", TenantID: "target", ServerID: "local", Risk: RiskHigh, Steps: []string{"preflight"}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "lifecycle") {
		t.Fatal("旧JSON新增字段")
	}
	old, err := ReleaseApprovalDigest(legacy)
	if err != nil || old != "sha256:"+WorkDigest(string(raw)) {
		t.Fatal("旧摘要改变")
	}
	binding := releaseBindingFixture()
	current := legacy
	current.SchemaVersion = 2
	current.Lifecycle = &binding
	digest, err := ReleaseApprovalDigest(current)
	if err != nil || len(digest) != 64 {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(current)
	if digest != "24d107305bea8cc38d4ecdf54b6abb5dc69d1b5d73288bc2e208f9c67bad2295" {
		t.Fatal("v2 编码黄金摘要改变", digest)
	}
	var decoded ApprovalSummary
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if got, e := ReleaseApprovalDigest(decoded); e != nil || got != digest {
		t.Fatal("往返改摘要", e)
	}
	targets, err := decoded.Lifecycle.WorkTargets()
	if err != nil || targets[1].ExpectedGeneration != math.MaxInt64 {
		t.Fatal("代次精度丢失")
	}
	decoded.Lifecycle.Targets[1].ExpectedGeneration = "3"
	if got, e := ReleaseApprovalDigest(decoded); e != nil || got == digest {
		t.Fatal("摘要未绑定代次")
	}
}

func TestReleaseLifecycleRejectsUnknownOrPartialBinding(t *testing.T) {
	for _, bad := range []string{"0", "-1", "01", "+1", "1.0", "1e3", "9223372036854775808", ""} {
		b := releaseBindingFixture()
		b.Targets[1].ExpectedGeneration = bad
		if _, err := b.WorkTargets(); err == nil {
			t.Fatal("接受非规范代次", bad)
		}
	}
	for _, mutation := range []string{"missing", "extra", "object", "mode", "source", "scope", "creator"} {
		t.Run(mutation, func(t *testing.T) {
			b := releaseBindingFixture()
			switch mutation {
			case "missing":
				b.Targets = b.Targets[:1]
			case "extra":
				b.Targets = append(b.Targets, ReleaseGeneration{TenantID: "zz", ExpectedGeneration: "1"})
			case "object":
				b.TargetObjects[0].TenantID = "unknown"
			case "mode":
				b.ExecutionMode = "remote"
			case "source":
				b.Source = "batch"
			case "scope":
				b.ScopeDigest = ""
			case "creator":
				b.CreatorTenantID = "missing"
			}
			if _, err := b.WorkTargets(); err == nil {
				t.Fatal("接受不完整绑定")
			}
		})
	}
	for _, version := range []int{0, 3, 50} {
		if _, err := ReleaseApprovalDigest(ApprovalSummary{SchemaVersion: version}); err == nil {
			t.Fatal("接受未知摘要版本")
		}
	}
}
