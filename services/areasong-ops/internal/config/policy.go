package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type FleetPolicy struct {
	Enabled                 bool              `json:"enabled"`
	HeartbeatTimeoutSeconds int               `json:"heartbeatTimeoutSeconds"`
	HeartbeatMaxSkewSeconds int               `json:"heartbeatMaxSkewSeconds,omitempty"`
	AllowRemoteRunners      bool              `json:"allowRemoteRunners"`
	RequiremTLS             bool              `json:"requireMTLS"`
	RequireSignedHeartbeat  bool              `json:"requireSignedHeartbeat,omitempty"`
	MTLSListenAddress       string            `json:"mtlsListenAddress,omitempty"`
	MTLSCertificateFile     string            `json:"mtlsCertificateFile,omitempty"`
	MTLSKeyFile             string            `json:"mtlsKeyFile,omitempty"`
	MTLSClientCAFile        string            `json:"mtlsClientCAFile,omitempty"`
	RunnerPublicKeys        map[string]string `json:"runnerPublicKeys,omitempty"`
	Inventory               model.Fleet       `json:"inventory,omitempty"`
}

type ExtensionPolicy struct {
	Enabled              bool              `json:"enabled"`
	TrustedPublishers    []string          `json:"trustedPublishers,omitempty"`
	TrustedPublisherKeys map[string]string `json:"trustedPublisherKeys,omitempty"`
	RequireSignature     bool              `json:"requireSignature"`
	Sandbox              string            `json:"sandbox"`
	MaxPackageBytes      int64             `json:"maxPackageBytes,omitempty"`
}

type TerminalPolicy struct {
	Enabled           bool                             `json:"enabled"`
	BreakGlass        bool                             `json:"breakGlass"`
	Commands          map[string]model.TerminalCommand `json:"commands,omitempty"`
	MaxSessionSeconds int                              `json:"maxSessionSeconds"`
	ShellExecutable   string                           `json:"shellExecutable,omitempty"`
	ShellWorkingDir   string                           `json:"shellWorkingDirectory,omitempty"`
}

type FilePolicy struct {
	Enabled      bool              `json:"enabled"`
	ReadOnly     bool              `json:"readOnly"`
	Roots        map[string]string `json:"roots,omitempty"`
	MaxFileBytes int64             `json:"maxFileBytes"`
}

type RunnerUpdatePolicy struct {
	Enabled              bool              `json:"enabled"`
	RunnerID             string            `json:"runnerId"`
	ArtifactRoot         string            `json:"artifactRoot"`
	BinaryPath           string            `json:"binaryPath,omitempty"`
	UnitName             string            `json:"unitName,omitempty"`
	UpdaterUnitName      string            `json:"updaterUnitName,omitempty"`
	Publisher            string            `json:"publisher,omitempty"`
	TrustedPublisherKeys map[string]string `json:"trustedPublisherKeys,omitempty"`
	ManifestPurpose      string            `json:"manifestPurpose,omitempty"`
	ManifestSchema       int               `json:"manifestSchema,omitempty"`
	ManifestGOOS         string            `json:"manifestGoos,omitempty"`
	ManifestGOARCH       string            `json:"manifestGoarch,omitempty"`
	HealthTimeoutSeconds int               `json:"healthTimeoutSeconds,omitempty"`
	MaxArtifactBytes     int64             `json:"maxArtifactBytes,omitempty"`
	LeaseSeconds         int               `json:"leaseSeconds,omitempty"`
}

const (
	// RunnerUpdateManifestPurpose is intentionally distinct from extension and
	// service manifests, preventing cross-purpose signature replay.
	RunnerUpdateManifestPurpose = "areasong-ops.runner-update"
	RunnerUpdateManifestSchema  = 1
	RunnerUpdateManifestGOOS    = "linux"
	RunnerUpdateManifestGOARCH  = "amd64"
	RunnerUpdateLeaseSeconds    = 120
)

// AccessPolicy is optional for backwards-compatible schema-4 catalogs. When
// Enforced is true, every mutating request must match a tenant-scoped binding.
type AccessPolicy struct {
	Enforced      bool                       `json:"enforced"`
	DefaultTenant string                     `json:"defaultTenant,omitempty"`
	Principals    map[string]AccessPrincipal `json:"principals,omitempty"`
	Tenants       map[string]model.Tenant    `json:"tenants,omitempty"`
	Roles         map[string]model.Role      `json:"roles,omitempty"`
	Bindings      []model.RoleBinding        `json:"bindings,omitempty"`
}

type AccessPrincipal struct {
	Email     string     `json:"email,omitempty"`
	EmailHash string     `json:"emailHash,omitempty"`
	Subject   string     `json:"subject,omitempty"`
	TenantID  string     `json:"tenantId"`
	Roles     []string   `json:"roles"`
	Status    string     `json:"status,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	JIT       bool       `json:"jit,omitempty"`
}

var accessHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (policy *AccessPolicy) Normalize() error {
	if policy == nil {
		return nil
	}
	if policy.DefaultTenant == "" {
		policy.DefaultTenant = "default"
	}
	policy.DefaultTenant = NormalizeAccessSubject(policy.DefaultTenant)
	normalized := make(map[string]AccessPrincipal, len(policy.Principals))
	for key, principal := range policy.Principals {
		key = canonicalAccessSubject(key)
		principal.Email = NormalizeAccessSubject(principal.Email)
		if principal.Email != "" {
			computed := AccessHashForEmail(principal.Email)
			if principal.EmailHash != "" && NormalizeAccessSubject(principal.EmailHash) != computed {
				return fmt.Errorf("访问主体 %s 的邮箱与哈希不一致", key)
			}
			principal.EmailHash = computed
		}
		if principal.EmailHash != "" {
			principal.EmailHash = NormalizeAccessSubject(principal.EmailHash)
			if !IsAccessHash(principal.EmailHash) {
				return fmt.Errorf("访问主体 %s 的 emailHash 无效", key)
			}
		}
		if principal.Subject != "" {
			principal.Subject = canonicalAccessSubject(principal.Subject)
			if !IsAccessHash(principal.Subject) {
				return fmt.Errorf("访问主体 %s 的 subject 无效", key)
			}
		}
		if principal.TenantID == "" {
			principal.TenantID = policy.DefaultTenant
		}
		principal.TenantID = NormalizeAccessSubject(principal.TenantID)
		if principal.Status == "" {
			principal.Status = "active"
		}
		if principal.Status != "active" && principal.Status != "disabled" && principal.Status != "suspended" {
			return fmt.Errorf("访问主体 %s 的状态无效", key)
		}
		if principal.ExpiresAt != nil && !principal.ExpiresAt.IsZero() && principal.ExpiresAt.Location() != time.UTC {
			value := principal.ExpiresAt.UTC()
			principal.ExpiresAt = &value
		}
		canonical := principal.EmailHash
		if canonical == "" {
			canonical = principal.Subject
		}
		if canonical == "" {
			canonical = key
		}
		if !IsAccessHash(canonical) || key != canonical {
			return fmt.Errorf("访问主体 key 与规范 subject 不一致: %s", key)
		}
		if principal.EmailHash != "" && principal.EmailHash != canonical {
			return fmt.Errorf("访问主体 %s 的 emailHash 与 subject 不一致", key)
		}
		if principal.Subject != "" && principal.Subject != canonical {
			return fmt.Errorf("访问主体 %s 的 subject 与 key 不一致", key)
		}
		if _, exists := normalized[canonical]; exists {
			return fmt.Errorf("访问主体规范化后重复: %s", canonical)
		}
		principal.Subject = canonical
		normalized[canonical] = principal
	}
	policy.Principals = normalized
	seenBindings := make(map[string]struct{}, len(policy.Bindings))
	for index := range policy.Bindings {
		binding := &policy.Bindings[index]
		binding.ID = strings.TrimSpace(binding.ID)
		if binding.ID == "" {
			return errors.New("角色绑定缺少标识")
		}
		if _, exists := seenBindings[binding.ID]; exists {
			return fmt.Errorf("角色绑定标识重复: %s", binding.ID)
		}
		seenBindings[binding.ID] = struct{}{}
		binding.Subject = canonicalAccessSubject(binding.Subject)
		if !IsAccessHash(binding.Subject) {
			return fmt.Errorf("角色绑定 %s 的主体无效", binding.ID)
		}
		binding.TenantID = NormalizeAccessSubject(binding.TenantID)
		binding.RoleID = NormalizeAccessSubject(binding.RoleID)
		binding.CreatedBy = "bootstrap"
	}
	return nil
}

func canonicalAccessSubject(value string) string {
	value = NormalizeAccessSubject(value)
	if strings.Contains(value, "@") {
		return AccessHashForEmail(value)
	}
	return value
}

func NormalizeAccessSubject(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func AccessHashForEmail(email string) string {
	hash := sha256.Sum256([]byte(NormalizeAccessSubject(email)))
	return hex.EncodeToString(hash[:])
}

func IsAccessHash(value string) bool {
	return accessHashPattern.MatchString(NormalizeAccessSubject(value))
}
