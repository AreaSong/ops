package runner

import (
	"context"
	"encoding/json"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// 端点能力来自实际客户端装配；配置中写一个 URL 不能授予该能力。
type releaseCoordinatorVerifier interface {
	verifyReleaseCoordinator(context.Context, releaseCoordinatorInput) error
}
type releaseCoordinatorInput struct {
	manager               Alertmanager
	stateRoot, backupRoot string
}

type sub2apiAlertmanagerEndpoint interface{ sub2apiEndpoint() string }

func (p *sub2apiInspectionProfile) verifyReleaseCoordinator(ctx context.Context, input releaseCoordinatorInput) error {
	if p == nil || p.backend == nil || ctx.Err() != nil {
		return errSub2API
	}
	identity, ok := input.manager.(sub2apiAlertmanagerEndpoint)
	if !ok {
		return errSub2API
	}
	b, err := sub2apiRegular(p.manifestPath)
	var m sub2apiManifest
	if err != nil || model.WorkDigest(string(b)) != p.manifestDigest || decodeSub2API(b, &m) != nil || identity.sub2apiEndpoint() != m.Alerts.Endpoint ||
		m.Resources["operation-root"].Selector != filepath.Join(input.stateRoot, "operations") || m.Resources["inspection-root"].Selector != input.stateRoot || m.Resources["backup-root"].Selector != input.backupRoot {
		return errSub2API
	}
	return nil
}

type sub2apiAlertScope struct {
	Endpoint                          string
	Matchers                          map[string]string
	BlockingAlerts, MaintenanceAlerts []string
}

func (m sub2apiManifest) validateExternalScopes(s model.ServiceDefinition) error {
	a := m.Alerts
	u, err := url.Parse(a.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errSub2API
	}
	if !reflect.DeepEqual(a.Matchers, s.AlertPolicy.Matchers) || !reflect.DeepEqual(a.BlockingAlerts, s.AlertPolicy.BlockingAlerts) ||
		!reflect.DeepEqual(a.MaintenanceAlerts, s.AlertPolicy.MaintenanceAlerts) || a.Matchers["service"] != s.Name ||
		a.Matchers["tenantId"] != s.TenantID || a.Matchers["serverId"] != s.ServerID || a.Matchers["objectId"] != s.ObjectID ||
		len(a.BlockingAlerts) == 0 || len(a.MaintenanceAlerts) == 0 || m.Resources["alertmanager"].Selector != sub2apiDigest(a) {
		return errSub2API
	}
	for _, labels := range [][]string{a.BlockingAlerts, a.MaintenanceAlerts} {
		seen := map[string]bool{}
		for _, label := range labels {
			if !sub2apiPlain(label) || seen[label] {
				return errSub2API
			}
			seen[label] = true
		}
	}
	if !reflect.DeepEqual(m.Nginx, s.TrafficPolicy) || m.Resources["nginx"].Selector != sub2apiDigest(m.Nginx) {
		return errSub2API
	}
	paths := []string{}
	if m.Nginx != nil {
		if m.Nginx.AdapterPath != model.TrafficAdapterPath || !sub2apiPlain(m.Nginx.Hostname) {
			return errSub2API
		}
		paths = []string{m.Nginx.SiteFile, m.Nginx.IncludeFile, m.Nginx.MaintenanceFile}
	}
	if len(m.NginxFiles) != len(paths) {
		return errSub2API
	}
	for _, path := range paths {
		b, err := sub2apiRegular(path)
		if err != nil || model.WorkDigest(string(b)) != m.NginxFiles[path] {
			return errSub2API
		}
	}
	// 额外 adapter 字段不参与这个七阶段固定入口，拒绝动态分派混入。
	r := m.Runtime
	if r.RestoreExecutable != "" || r.RestoreDrillExecutable != "" || r.PrepareExecutable != "" || len(r.BackupExecutables) != 0 || r.BackupEvidenceExecutable != "" {
		return errSub2API
	}
	// 不可变实现根不能与本次可写根重叠；摘要复核不代替后端的实际隔离。
	for _, role := range []string{"controlled-compose", "runtime-compose", "environment", "backup-root", "operation-root", "temporary-root", "lock", "metrics", "log"} {
		path := m.Resources[role].Selector
		if path == m.BundleRoot || strings.HasPrefix(path, m.BundleRoot+string(filepath.Separator)) || strings.HasPrefix(m.BundleRoot, path+string(filepath.Separator)) {
			return errSub2API
		}
	}
	return nil
}

// 协调对象仅授予互斥/阻断能力，不授予其他应用的数据范围。
func sub2apiCoordinationRole(role string) bool {
	return role == "coordination-root" || role == "postgres-lock" || role == "redis-lock" || role == "volumes-lock"
}
func (m sub2apiManifest) validateCoordination(s model.ServiceDefinition) error {
	for _, role := range []string{"postgres-lock", "redis-lock", "volumes-lock", "coordination-root"} {
		r := m.Resources[role]
		if !sub2apiPath(r.Selector) || !sub2apiPlain(r.ObjectID) || r.ObjectID == s.ObjectID {
			return errSub2API
		}
		if role != "coordination-root" && filepath.Base(r.Selector) != "ops-backup-"+strings.TrimSuffix(role, "-lock")+".lock" {
			return errSub2API
		}
		if r.Selector == m.BundleRoot || strings.HasPrefix(r.Selector, m.BundleRoot+string(filepath.Separator)) {
			return errSub2API
		}
	}
	return nil
}

func validateSub2APIDatabaseDocument(postgres, redis json.RawMessage) error {
	var pg struct {
		SystemIdentifier    string   `json:"systemIdentifier"`
		User                string   `json:"user"`
		MaintenanceDatabase string   `json:"maintenanceDatabase"`
		Databases           []string `json:"databases"`
		Roles               []string `json:"roles"`
		ToolPath            string   `json:"toolPath"`
		Version             string   `json:"version"`
		CatalogDigest       string   `json:"catalogDigest"`
	}
	var rd struct {
		RunID   string `json:"runId"`
		Version string `json:"version"`
	}
	if decodeSub2APIBackup(postgres, &pg) != nil || decodeSub2APIBackup(redis, &rd) != nil {
		return errSub2API
	}
	safe := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	for _, value := range []string{pg.SystemIdentifier, pg.User, pg.MaintenanceDatabase, pg.Version, rd.RunID, rd.Version} {
		if !safe.MatchString(value) {
			return errSub2API
		}
	}
	if pg.ToolPath != "/usr/local/bin:/usr/bin:/bin" || !model.ValidWorkDigest(pg.CatalogDigest) {
		return errSub2API
	}
	for _, values := range [][]string{pg.Databases, pg.Roles} {
		seen := map[string]bool{}
		if len(values) == 0 {
			return errSub2API
		}
		for _, value := range values {
			if !safe.MatchString(value) || seen[value] {
				return errSub2API
			}
			seen[value] = true
		}
	}
	if !containsSub2APIValue(pg.Databases, pg.MaintenanceDatabase) || !containsSub2APIValue(pg.Roles, pg.User) || containsSub2APIValue(pg.Roles, "postgres") {
		return errSub2API
	}
	return nil
}
func containsSub2APIValue(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
