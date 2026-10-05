package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const sub2apiProfileID = "sub2api-local-update-v1"

var sub2apiGitRevision = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

var errSub2API = errors.New("Sub2API 私有协议缺证或不匹配，保持未收敛")
var sub2apiPhases = []string{"preflight", "backup", "migration", "apply", "health", "smoke", "identity"}

// B1 消费受控解析后的完整 manifest；不把 shell 文本、catalog 或客户端声明当作实际资源。
// 每个 selector 均由后端重新解析/观测并逐项比较，无法表达的拓扑必须拒绝。
type sub2apiResource struct {
	Selector string
	ObjectID string
	TenantID string
	ServerID string
}

type sub2apiFile struct {
	Digest  string
	Imports []string
}

type sub2apiState struct {
	Controlled, Runtime                        string
	Image, ImageID, Version, Commit, Migration string
}

type sub2apiManifest struct {
	Alerts                                           sub2apiAlertScope
	Nginx                                            *model.TrafficPolicy
	NginxFiles                                       map[string]string
	Version                                          int
	Profile, Target, MaterialKind, BundleRoot        string
	Runtime                                          model.ComposeServiceRuntime
	Resources                                        map[string]sub2apiResource
	Files                                            map[string]sub2apiFile
	Environment                                      map[string]string
	EnvironmentFileDigest                            string
	Before, TargetState                              sub2apiState
	BeforeImageBundleDigest, TargetImageBundleDigest string
	SourceDigest                                     string
	Prepared, Source, Rehearsal                      string
	BackupMode                                       string
}

var sub2apiResourceRoles = strings.Fields("daemon project service container postgres redis database redis-instance controlled-compose runtime-compose environment app-mount postgres-mount redis-mount network health-endpoint smoke-endpoint backup-root operation-root inspection-root temporary-root lock metrics log nginx alertmanager catalog prepared source rehearsal postgres-lock redis-lock volumes-lock coordination-root")
var sub2apiPathRoles = strings.Fields("controlled-compose runtime-compose environment app-mount postgres-mount redis-mount backup-root operation-root inspection-root temporary-root lock metrics log catalog prepared source rehearsal postgres-lock redis-lock volumes-lock coordination-root")

func sub2apiDigest(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return model.WorkDigest(string(b))
}

func decodeSub2API(b []byte, v any) error {
	if len(b) == 0 || len(b) > 1<<20 {
		return errSub2API
	}
	if rejectSub2APIDuplicateKeys(json.NewDecoder(bytes.NewReader(b))) != nil {
		return errSub2API
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(&struct{}{}) != io.EOF {
		return errSub2API
	}
	return nil
}

func rejectSub2APIDuplicateKeys(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return errSub2API
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return errSub2API
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errSub2API
			}
			seen[name] = true
		}
		if rejectSub2APIDuplicateKeys(d) != nil {
			return errSub2API
		}
	}
	_, err = d.Token()
	return err
}

func sub2apiPlain(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\n\r")
}

func sub2apiPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/"
}

// 父目录也不可为链接；此检查不消除 TOCTOU，执行仍须后端的不可替换输入证明。
func sub2apiFileInfo(path string) (os.FileInfo, error) {
	if !sub2apiPath(path) {
		return nil, errSub2API
	}
	for p := path; p != string(filepath.Separator); p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil || st.Mode()&os.ModeSymlink != 0 {
			return nil, errSub2API
		}
		if p == path && !st.Mode().IsRegular() {
			return nil, errSub2API
		}
	}
	return os.Lstat(path)
}

func sub2apiRegular(path string) ([]byte, error) {
	st, err := sub2apiFileInfo(path)
	if err != nil || st.Size() > 1<<20 {
		return nil, errSub2API
	}
	return os.ReadFile(path)
}

func sub2apiFileDigest(path string) (string, error) {
	if _, err := sub2apiFileInfo(path); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (m sub2apiManifest) validate(service model.ServiceDefinition, target string) error {
	if m.Version != 1 || m.Profile != sub2apiProfileID || m.Target != target || !sub2apiPlain(target) ||
		(m.MaterialKind != "synthetic" && m.MaterialKind != "offline") || !sub2apiPath(m.BundleRoot) ||
		service.Name != "sub2api" || service.Runtime == nil || service.ReleaseScope == nil ||
		service.ReleaseScope.ProfileID != sub2apiProfileID || !reflect.DeepEqual(*service.Runtime, m.Runtime) ||
		service.Adapter != filepath.Join(m.BundleRoot, sub2apiCompose) || m.BackupMode != "service-exclusive-v1" {
		return errSub2API
	}
	if service.RecoveryPointPolicy == nil || !sameStringSet(service.RecoveryPointPolicy.RequiredArtifactRoles, []string{"postgres-sub2api", "redis", "volume-sub2api-data", "configs", "runtime-snapshot"}) {
		return errSub2API
	}
	if err := m.validateResources(service); err != nil {
		return err
	}
	if !validSub2APIState(m.Before) || !validSub2APIState(m.TargetState) || m.Before == m.TargetState ||
		m.TargetState.Version != strings.TrimPrefix(target, "v") ||
		m.Before.ImageID == m.TargetState.ImageID || m.Before.Controlled != m.Before.Runtime ||
		m.TargetState.Controlled != m.TargetState.Runtime || m.Before.Controlled == m.TargetState.Controlled {
		return errSub2API
	}
	if !reflect.DeepEqual(m.Environment, map[string]string{"LANG": "C", "LC_ALL": "C", "TZ": "UTC", "PATH": filepath.Join(m.BundleRoot, "tools")}) {
		return errSub2API
	}
	env, err := sub2apiRegular(m.Runtime.EnvFile)
	if err != nil || model.WorkDigest(string(env)) != m.EnvironmentFileDigest {
		return errSub2API
	}
	if err := m.validateExternalScopes(service); err != nil {
		return err
	}
	return m.validateBundle()
}

func validSub2APIState(s sub2apiState) bool {
	return model.ValidWorkDigest(s.Controlled) && model.ValidWorkDigest(s.Runtime) &&
		model.ValidWorkDigest(s.Migration) && sub2apiPlain(s.Image) &&
		strings.HasPrefix(s.ImageID, "sha256:") && model.ValidWorkDigest(strings.TrimPrefix(s.ImageID, "sha256:")) &&
		sub2apiPlain(s.Version) && sub2apiGitRevision.MatchString(s.Commit)
}

func (m sub2apiManifest) validateResources(s model.ServiceDefinition) error {
	if len(m.Resources) != len(sub2apiResourceRoles) {
		return errSub2API
	}
	for _, role := range sub2apiResourceRoles {
		r := m.Resources[role]
		// 首版只表达单对象专属资源；共享实例/跨租户资源不能套用此 profile。
		if !sub2apiPlain(r.Selector) || (!sub2apiCoordinationRole(role) && r.ObjectID != s.ObjectID) || r.TenantID != s.TenantID || r.ServerID != s.ServerID ||
			s.ObjectID == "" || s.TenantID == "" || s.ServerID == "" {
			return errSub2API
		}
	}
	if m.validateCoordination(s) != nil {
		return errSub2API
	}
	for _, role := range sub2apiPathRoles {
		if !sub2apiPath(m.Resources[role].Selector) {
			return errSub2API
		}
	}
	bindings := map[string]string{"controlled-compose": m.Runtime.ControlledCompose, "runtime-compose": m.Runtime.RuntimeCompose,
		"environment": m.Runtime.EnvFile, "project": m.Runtime.ProjectName, "service": m.Runtime.ApplicationService,
		"container": m.Runtime.ApplicationContainer, "health-endpoint": m.Runtime.HealthURL, "catalog": m.Runtime.ReleaseCatalog,
		"prepared": m.Runtime.PreparedReleaseDir, "source": m.Source, "rehearsal": m.Rehearsal}
	for role, value := range bindings {
		if m.Resources[role].Selector != value {
			return errSub2API
		}
	}
	if m.Runtime.ApplicationService != "sub2api" || m.Runtime.InspectExecutable != filepath.Join(m.BundleRoot, sub2apiWrapper) ||
		m.Runtime.UpdateExecutable != m.Runtime.InspectExecutable || m.Runtime.ControlledCompose == m.Runtime.RuntimeCompose ||
		!reflect.DeepEqual(m.Runtime.DependencyContainers, []string{m.Resources["postgres"].Selector, m.Resources["redis"].Selector}) {
		return errSub2API
	}
	// 共享默认根和 B2 之前的共享备份作业不能通过资源筛选伪装为限定作业。
	for _, role := range []string{"backup-root", "lock", "metrics", "log"} {
		if !strings.Contains(m.Resources[role].Selector, "/sub2api/") {
			return errSub2API
		}
	}
	return nil
}
