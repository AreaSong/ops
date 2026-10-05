package model

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

var ErrReleaseLifecycle = errors.New("发布生命周期合同无效，请重新提案")
var ErrReleaseNotIntegrated = errors.New("B-P 阶段尚未接入此普通发布操作；仅允许安全历史重放")

type ReleaseScopeResource struct {
	Kind     string `json:"kind"`
	Selector string `json:"selector"`
	ObjectID string `json:"objectId"`
}

// 声明只描述待核验范围，不构成真实 profile 或收敛证明。
type ReleaseScopeDefinition struct {
	Version               int                    `json:"version"`
	Mode                  string                 `json:"mode"`
	ProfileID             string                 `json:"profileId"`
	Resources             []ReleaseScopeResource `json:"resources"`
	ImplementationDigests map[string]string      `json:"implementationDigests"`
}

type ReleaseTargetObject struct {
	ObjectID string `json:"objectId"`
	TenantID string `json:"tenantId"`
	ServerID string `json:"serverId"`
}

type ReleaseGeneration struct {
	TenantID           string `json:"tenantId"`
	ExpectedGeneration string `json:"expectedGeneration"`
}

type ReleaseLifecycleBinding struct {
	Version         int                   `json:"version"`
	Source          string                `json:"source"`
	ExecutionMode   string                `json:"executionMode"`
	PreparationID   string                `json:"preparationId"`
	CreatorTenantID string                `json:"creatorTenantId"`
	TargetObjects   []ReleaseTargetObject `json:"targetObjects"`
	Targets         []ReleaseGeneration   `json:"targets"`
	ScopeDigest     string                `json:"scopeDigest"`
}

func (scope ReleaseScopeDefinition) Validate() error {
	if scope.Version != 1 || scope.Mode != "local" || !workIdentifier(scope.ProfileID) ||
		len(scope.Resources) == 0 || len(scope.ImplementationDigests) == 0 {
		return ErrReleaseLifecycle
	}
	seen := make(map[string]bool)
	for _, r := range scope.Resources {
		key := r.Kind + "\x00" + r.Selector
		if !workIdentifier(r.Kind) || !workIdentifier(r.Selector) || !workIdentifier(r.ObjectID) || seen[key] {
			return ErrReleaseLifecycle
		}
		seen[key] = true
	}
	for path, digest := range scope.ImplementationDigests {
		if !workIdentifier(path) || !ValidWorkDigest(digest) {
			return ErrReleaseLifecycle
		}
	}
	return nil
}

func (binding ReleaseLifecycleBinding) WorkTargets() ([]WorkAdmissionTarget, error) {
	if binding.Version != 1 || binding.Source != "manual_single" || binding.ExecutionMode != "local" ||
		!workIdentifier(binding.PreparationID) || !canonicalReleaseTenant(binding.CreatorTenantID) ||
		!ValidWorkDigest(binding.ScopeDigest) || len(binding.Targets) == 0 || len(binding.TargetObjects) == 0 {
		return nil, ErrReleaseLifecycle
	}
	targets := make([]WorkAdmissionTarget, 0, len(binding.Targets))
	tenants := make(map[string]bool)
	last := ""
	for _, t := range binding.Targets {
		n, err := strconv.ParseInt(t.ExpectedGeneration, 10, 64)
		if err != nil || n <= 0 || strconv.FormatInt(n, 10) != t.ExpectedGeneration ||
			!canonicalReleaseTenant(t.TenantID) || t.TenantID <= last {
			return nil, ErrReleaseLifecycle
		}
		last = t.TenantID
		tenants[t.TenantID] = true
		targets = append(targets, WorkAdmissionTarget{TenantID: t.TenantID, ExpectedGeneration: n})
	}
	if !tenants[binding.CreatorTenantID] {
		return nil, ErrReleaseLifecycle
	}
	last = ""
	required := map[string]bool{binding.CreatorTenantID: true}
	for _, object := range binding.TargetObjects {
		if !workIdentifier(object.ObjectID) || object.ObjectID <= last || !workIdentifier(object.ServerID) ||
			!tenants[object.TenantID] {
			return nil, ErrReleaseLifecycle
		}
		last = object.ObjectID
		required[object.TenantID] = true
	}
	if !reflect.DeepEqual(required, tenants) {
		return nil, ErrReleaseLifecycle
	}
	return targets, nil
}

func canonicalReleaseTenant(id string) bool {
	return workIdentifier(id) && id != "*" && strings.ToLower(id) == id
}

func ReleaseApprovalDigest(summary ApprovalSummary) (string, error) {
	if summary.SchemaVersion != 1 && summary.SchemaVersion != 2 {
		return "", ErrReleaseLifecycle
	}
	if summary.SchemaVersion == 1 && summary.Lifecycle != nil {
		return "", ErrReleaseLifecycle
	}
	if summary.SchemaVersion == 2 {
		switch summary.Risk {
		case RiskReadOnly, RiskLow, RiskMedium, RiskHigh:
		default:
			return "", ErrReleaseLifecycle
		}
		if summary.Service == "" || summary.Target == "" || summary.TenantID == "" || summary.ServerID == "" || len(summary.Steps) == 0 || summary.ApprovalException != "" {
			return "", ErrReleaseLifecycle
		}

		if summary.Lifecycle == nil || summary.Action != "update" || summary.RestoreMode != "" ||
			summary.RecoveryPointID != "" || summary.AutoUpdatePolicy != nil {
			return "", ErrReleaseLifecycle
		}
		if _, err := summary.Lifecycle.WorkTargets(); err != nil {
			return "", err
		}
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		return "", err
	}
	digest := WorkDigest(string(raw))
	if summary.SchemaVersion == 1 {
		digest = "sha256:" + digest
	}
	return digest, nil
}

func CanonicalReleaseScope(scope ReleaseScopeDefinition) (string, error) {
	if err := scope.Validate(); err != nil {
		return "", err
	}
	scope.Resources = append([]ReleaseScopeResource(nil), scope.Resources...)
	sort.Slice(scope.Resources, func(i, j int) bool {
		a, b := scope.Resources[i], scope.Resources[j]
		return a.Kind+"\x00"+a.Selector+"\x00"+a.ObjectID < b.Kind+"\x00"+b.Selector+"\x00"+b.ObjectID
	})
	raw, err := json.Marshal(scope)
	return string(raw), err
}
