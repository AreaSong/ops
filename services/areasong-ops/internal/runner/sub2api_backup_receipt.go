package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// 外层恢复点仍为 v1；格式、调用关联在摘要保护的 runtime v2 内。
type sub2apiBackupBinding struct {
	CallID               string `json:"callId"`
	TaskID               string `json:"taskId"`
	PlanID               string `json:"planId"`
	PreparationID        string `json:"preparationId"`
	LeaseID              string `json:"leaseId"`
	Target               string `json:"target"`
	OperationDir         string `json:"operationDir"`
	ScopeDigest          string `json:"scopeDigest"`
	ImplementationDigest string `json:"implementationDigest"`
	ParentReceiptDigest  string `json:"parentReceiptDigest"`
	ResourceDigest       string `json:"resourceDigest"`
}
type sub2apiBackupReference struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type sub2apiBackupRequest struct {
	sub2apiBackupBinding
	ProtocolVersion  int                    `json:"protocolVersion"`
	Mode             string                 `json:"mode"`
	Service          string                 `json:"service"`
	Job              string                 `json:"job"`
	ResourceManifest sub2apiBackupReference `json:"resourceManifest"`
	Deadline         int64                  `json:"deadline"`
}
type sub2apiBackupArtifact struct {
	Role      string `json:"role"`
	Path      string `json:"path"`
	Format    string `json:"format"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}
type sub2apiBackupChild struct {
	Job    string `json:"job"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type sub2apiBackupCollection struct {
	sub2apiBackupBinding
	ProtocolVersion int                   `json:"protocolVersion"`
	Service         string                `json:"service"`
	Job             string                `json:"job"`
	RequestDigest   string                `json:"requestDigest"`
	State           string                `json:"state"`
	Artifact        sub2apiBackupArtifact `json:"artifact"`
	Proof           string                `json:"proof"`
}
type sub2apiBackupSet struct {
	sub2apiBackupBinding
	ProtocolVersion int                     `json:"protocolVersion"`
	Service         string                  `json:"service"`
	Job             string                  `json:"job"`
	RequestDigest   string                  `json:"requestDigest"`
	State           string                  `json:"state"`
	StartedAt       int64                   `json:"startedAt"`
	EndedAt         int64                   `json:"endedAt"`
	Artifacts       []sub2apiBackupArtifact `json:"artifacts"`
	Subreceipts     []sub2apiBackupChild    `json:"subreceipts"`
	Proof           string                  `json:"proof"`
	Cleanup         string                  `json:"cleanup"`
	Category        string                  `json:"category"`
}
type sub2apiRuntimeContainer struct {
	Name            string `json:"name"`
	ConfiguredImage string `json:"configured_image"`
	ImageID         string `json:"image_id"`
	ContainerID     string `json:"container_id"`
	Version         string `json:"version"`
	Revision        string `json:"revision"`
}
type sub2apiRuntimeBackup struct {
	sub2apiBackupBinding
	ProtocolVersion int                     `json:"protocolVersion"`
	RequestDigest   string                  `json:"requestDigest"`
	Artifacts       []sub2apiBackupArtifact `json:"artifacts"`
	Subreceipts     []sub2apiBackupChild    `json:"subreceipts"`
	Proof           string                  `json:"proof"`
	Consistency     string                  `json:"consistency"`
}
type sub2apiRuntimeDatabase struct {
	User       string `json:"user"`
	Database   string `json:"database"`
	Migrations int    `json:"migrations"`
}
type sub2apiRuntimeBaseline struct {
	Containers map[string]sub2apiRuntimeContainer `json:"containers"`
	Database   sub2apiRuntimeDatabase             `json:"database"`
}
type sub2apiRuntimeV2 struct {
	SchemaVersion int                                `json:"schemaVersion"`
	Service       string                             `json:"service"`
	Containers    map[string]sub2apiRuntimeContainer `json:"containers"`
	Database      sub2apiRuntimeDatabase             `json:"database"`
	Backup        sub2apiRuntimeBackup               `json:"backup"`
}

var sub2apiBackupJobs = []string{"postgres", "redis", "volumes", "config-capture", "set"}
var sub2apiBackupFormats = [][3]string{
	{"postgres-sub2api", "postgres.sql.gz", "pg-dumpall-sql-gzip-v1"},
	{"redis", "redis.tar.gz", "redis-rdb-acl-targz-v1"},
	{"volume-sub2api-data", "data.tar.gz", "sub2api-data-targz-v1"},
	{"configs", "configs.tar.gz", "sub2api-configs-targz-v1"},
	{"runtime-snapshot", "runtime.json", "sub2api-runtime-json-v2"},
}

// JSON 的字段大小写也必须完全匹配，拒绝 encoding/json 宽松别名。
func decodeSub2APIBackup(raw []byte, out any) error {
	if decodeSub2API(raw, out) != nil {
		return errSub2API
	}
	normalized, err := json.Marshal(out)
	if err != nil {
		return errSub2API
	}
	var original, canonical any
	if json.Unmarshal(raw, &original) != nil || json.Unmarshal(normalized, &canonical) != nil || !reflect.DeepEqual(original, canonical) {
		return errSub2API
	}
	return nil
}
func sub2apiPrivateFile(path string) ([]byte, error) {
	st, err := sub2apiFileInfo(path)
	if err != nil || st.Mode().Perm() != 0600 {
		return nil, errSub2API
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return nil, errSub2API
	}
	return sub2apiRegular(path)
}
func sub2apiBackupExpected(r sub2apiCallRequest, resource string) sub2apiBackupBinding {
	return sub2apiBackupBinding{r.CallID, r.TaskID, r.PlanID, r.PreparationID, r.LeaseID, r.Target, r.OperationDir, r.ScopeDigest, r.ImplementationDigest, r.Previous, resource}
}

func validateSub2APIBackup(m sub2apiManifest, req sub2apiCallRequest, c sub2apiCompletion) error {
	if !model.ValidWorkDigest(c.BackupReceiptDigest) || !model.ValidWorkDigest(c.BackupResourceDigest) || c.RecoveryPoint == nil {
		return errSub2API
	}
	raw, err := sub2apiPrivateFile(filepath.Join(req.OperationDir, "backup-request.json"))
	var request sub2apiBackupRequest
	if err != nil || decodeSub2APIBackup(raw, &request) != nil {
		return errSub2API
	}
	if request.sub2apiBackupBinding != sub2apiBackupExpected(req, request.ResourceDigest) || request.ProtocolVersion != 1 || request.Mode != "service-exclusive-v1" || request.Service != "sub2api" || request.Job != "sub2api-set" || !model.ValidWorkDigest(request.ResourceDigest) {
		return errSub2API
	}
	if request.ResourceManifest.Path != filepath.Join(req.OperationDir, "backup-resources.json") || request.ResourceManifest.SHA256 != request.ResourceDigest || request.ResourceDigest != c.BackupResourceDigest {
		return errSub2API
	}
	resources, err := sub2apiPrivateFile(request.ResourceManifest.Path)
	if err != nil || model.WorkDigest(string(resources)) != request.ResourceDigest {
		return errSub2API
	}
	if validateSub2APIBackupResources(m, req, resources) != nil {
		return errSub2API
	}
	if validateSub2APIBackupSource(resources, c.BackupSource) != nil || validateSub2APIResourceShape(resources, m, req) != nil || validateSub2APIBackupControl(m) != nil {
		return errSub2API
	}
	checksum := model.WorkDigest(string(raw))
	raw, err = sub2apiPrivateFile(filepath.Join(req.OperationDir, "backup-result.json"))
	var result sub2apiBackupSet
	if err != nil || model.WorkDigest(string(raw)) != c.BackupReceiptDigest || decodeSub2APIBackup(raw, &result) != nil {
		return errSub2API
	}
	if result.sub2apiBackupBinding != request.sub2apiBackupBinding || result.RequestDigest != checksum || result.ProtocolVersion != 1 || result.Service != "sub2api" || result.Job != "sub2api-set" || result.State != "completed" || result.Category != "none" || result.Cleanup != "retained" || !model.ValidWorkDigest(result.Proof) {
		return errSub2API
	}
	if result.StartedAt <= 0 || result.EndedAt < result.StartedAt || result.EndedAt > request.Deadline || result.EndedAt > time.Now().Add(time.Minute).Unix() {
		return errSub2API
	}
	root := filepath.Join(m.Resources["backup-root"].Selector, req.TaskID, req.CallID)
	if err := validateSub2APIBackupFiles(root, request, result, *c.RecoveryPoint); err != nil {
		return err
	}
	runtimeRaw, err := sub2apiPrivateFile(filepath.Join(root, "runtime.json"))
	var runtime sub2apiRuntimeV2
	if err != nil || decodeSub2APIBackup(runtimeRaw, &runtime) != nil {
		return errSub2API
	}
	if !reflect.DeepEqual(sub2apiRuntimeBaseline{runtime.Containers, runtime.Database}, c.BackupSource) {
		return errSub2API
	}
	app := runtime.Containers["app"]
	if app.ConfiguredImage != m.Before.Image || app.ImageID != m.Before.ImageID || app.Version != m.Before.Version || app.Revision != m.Before.Commit {
		return errSub2API
	}
	if m.MaterialKind != "synthetic" && runtime.Backup.Consistency == "synthetic-coordinated" {
		return errSub2API
	}
	return nil
}

func validateSub2APIBackupFiles(root string, request sub2apiBackupRequest, result sub2apiBackupSet, point model.RecoveryPointEvidence) error {
	if len(result.Artifacts) != 5 || len(result.Subreceipts) != 4 || len(point.Artifacts) != 5 || point.SchemaVersion != 1 || point.Service != "sub2api" || point.TaskID != request.TaskID {
		return errSub2API
	}
	actual := map[string]model.RecoveryArtifact{}
	for _, a := range point.Artifacts {
		if _, exists := actual[a.Role]; exists {
			return errSub2API
		}
		actual[a.Role] = a
	}
	for i, a := range result.Artifacts {
		f := sub2apiBackupFormats[i]
		if a.Role != f[0] || a.Path != f[1] || a.Format != f[2] || a.SizeBytes <= 0 || !strings.HasPrefix(a.SHA256, "sha256:") || !model.ValidWorkDigest(strings.TrimPrefix(a.SHA256, "sha256:")) {
			return errSub2API
		}
		path := filepath.Join(root, a.Path)
		st, err := sub2apiFileInfo(path)
		if err != nil || st.Mode().Perm() != 0600 || st.Size() != a.SizeBytes {
			return errSub2API
		}
		info, ok := st.Sys().(*syscall.Stat_t)
		if !ok || info.Uid != uint32(os.Geteuid()) || info.Nlink != 1 {
			return errSub2API
		}
		sum, err := sub2apiFileDigest(path)
		if err != nil || "sha256:"+sum != a.SHA256 || actual[a.Role] != (model.RecoveryArtifact{Role: a.Role, Path: path, SizeBytes: a.SizeBytes, SHA256: a.SHA256}) {
			return errSub2API
		}
		if i < 4 && validateSub2APIBackupChild(root, request, result, i) != nil {
			return errSub2API
		}
	}
	raw, err := sub2apiPrivateFile(filepath.Join(root, "runtime.json"))
	var runtime sub2apiRuntimeV2
	if err != nil || decodeSub2APIBackup(raw, &runtime) != nil {
		return errSub2API
	}
	b := runtime.Backup
	if b.sub2apiBackupBinding != request.sub2apiBackupBinding || b.RequestDigest != result.RequestDigest || b.ProtocolVersion != 1 || b.Proof != result.Proof || !reflect.DeepEqual(b.Artifacts, result.Artifacts[:4]) || !reflect.DeepEqual(b.Subreceipts, result.Subreceipts) {
		return errSub2API
	}
	if runtime.SchemaVersion != 2 || runtime.Service != "sub2api" || len(runtime.Containers) != 3 || runtime.Database.Migrations < 0 || !sub2apiPlain(runtime.Database.User) || !sub2apiPlain(runtime.Database.Database) {
		return errSub2API
	}
	if b.Consistency != "coordinated" && b.Consistency != "synthetic-coordinated" {
		return errSub2API
	}
	for _, role := range []string{"app", "postgres", "redis"} {
		v, ok := runtime.Containers[role]
		if !ok || !sub2apiPlain(v.Name) || !sub2apiPlain(v.ConfiguredImage) || !strings.HasPrefix(v.ImageID, "sha256:") || !model.ValidWorkDigest(strings.TrimPrefix(v.ImageID, "sha256:")) || !model.ValidWorkDigest(v.ContainerID) || !sub2apiPlain(v.Version) || !sub2apiGitRevision.MatchString(v.Revision) {
			return errSub2API
		}
	}
	return nil
}

func validateSub2APIBackupChild(root string, r sub2apiBackupRequest, result sub2apiBackupSet, i int) error {
	child := result.Subreceipts[i]
	job := sub2apiBackupJobs[i]
	if child.Job != job || child.Path != job+".receipt.json" {
		return errSub2API
	}
	raw, err := sub2apiPrivateFile(filepath.Join(root, child.Path))
	var v sub2apiBackupCollection
	if err != nil || "sha256:"+model.WorkDigest(string(raw)) != child.SHA256 || decodeSub2APIBackup(raw, &v) != nil {
		return errSub2API
	}
	if v.sub2apiBackupBinding != r.sub2apiBackupBinding || v.ProtocolVersion != 1 || v.Service != "sub2api" || v.Job != job || v.RequestDigest != result.RequestDigest || v.State != "completed" || v.Artifact != result.Artifacts[i] || !model.ValidWorkDigest(v.Proof) {
		return errSub2API
	}
	return nil
}

func validateSub2APIBackupResources(m sub2apiManifest, request sub2apiCallRequest, raw []byte) error {
	// 资源观察真实性仍由私有 backend 证明；此处只复核映射与本次已批准 scope。
	var document map[string]json.RawMessage
	if decodeSub2API(raw, &document) != nil {
		return errSub2API
	}
	var resources map[string]struct {
		Selector string   `json:"selector"`
		ObjectID string   `json:"objectId"`
		TenantID string   `json:"tenantId"`
		ServerID string   `json:"serverId"`
		Actions  []string `json:"actions"`
	}
	if decodeSub2APIBackup(document["resources"], &resources) != nil {
		return errSub2API
	}
	mapping := map[string]string{"app": "app-mount", "rdb": "redis-mount", "acl": "redis-mount", "controlled": "controlled-compose", "runtime": "runtime-compose", "env": "environment", "backup": "backup-root", "temporary": "temporary-root", "log": "log", "metrics": "metrics", "coordination": "coordination-root", "postgres-lock": "postgres-lock", "redis-lock": "redis-lock", "volumes-lock": "volumes-lock", "operation": "operation-root"}
	if len(resources) != len(mapping) {
		return errSub2API
	}
	for key, role := range mapping {
		value, ok := resources[key]
		expected := m.Resources[role]
		path := expected.Selector
		if key == "rdb" {
			path = filepath.Join(path, "dump.rdb")
		}
		if key == "acl" {
			path = filepath.Join(path, "users.acl")
		}
		if key == "operation" {
			if filepath.Dir(value.Selector) != path || value.Selector != request.OperationDir {
				return errSub2API
			}
			path = value.Selector
		}
		action := "write"
		if key == "app" || key == "rdb" || key == "acl" || key == "controlled" || key == "runtime" || key == "env" {
			action = "read"
		}
		if strings.HasSuffix(key, "-lock") {
			action = "coordinate"
		}
		if !ok || value.Selector != path || value.ObjectID != expected.ObjectID || value.TenantID != expected.TenantID || value.ServerID != expected.ServerID || !reflect.DeepEqual(value.Actions, []string{action}) {
			return errSub2API
		}
	}
	return nil
}

func validateSub2APIBackupControl(m sub2apiManifest) error {
	raw, err := sub2apiRegular(filepath.Join(m.BundleRoot, "scripts/backup/backup-control.json"))
	var control struct {
		SchemaVersion    int    `json:"schemaVersion"`
		Mode             string `json:"mode"`
		CoordinationRoot string `json:"coordinationRoot"`
		InstanceDigest   string `json:"instanceDigest"`
	}
	if err != nil || decodeSub2APIBackup(raw, &control) != nil || control.SchemaVersion != 1 || control.Mode != "coordinated" || control.CoordinationRoot != m.Resources["coordination-root"].Selector || !model.ValidWorkDigest(control.InstanceDigest) {
		return errSub2API
	}
	return nil
}
func validateSub2APIBackupSource(raw []byte, source sub2apiRuntimeBaseline) error {
	var document map[string]json.RawMessage
	if decodeSub2API(raw, &document) != nil {
		return errSub2API
	}
	keys := []string{"schemaVersion", "materialKind", "target", "identity", "containers", "postgres", "redis", "paths", "roots", "locks", "resources", "runtime"}
	if len(document) != len(keys) {
		return errSub2API
	}
	for _, key := range keys {
		if _, ok := document[key]; !ok {
			return errSub2API
		}
	}
	var baseline sub2apiRuntimeBaseline
	if decodeSub2APIBackup(document["runtime"], &baseline) != nil || !reflect.DeepEqual(baseline, source) {
		return errSub2API
	}
	var containers map[string]struct {
		ID         string `json:"id"`
		Generation string `json:"generation"`
	}
	if decodeSub2APIBackup(document["containers"], &containers) != nil || len(containers) != 3 {
		return errSub2API
	}
	for role, container := range source.Containers {
		if containers[role].ID != container.ContainerID || !sub2apiPlain(containers[role].Generation) {
			return errSub2API
		}
	}
	return nil
}

func sub2apiJSONFields(raw []byte, keys string) (map[string]json.RawMessage, error) {
	var value map[string]json.RawMessage
	if decodeSub2API(raw, &value) != nil || len(value) != len(strings.Fields(keys)) {
		return nil, errSub2API
	}
	for _, key := range strings.Fields(keys) {
		if _, ok := value[key]; !ok {
			return nil, errSub2API
		}
	}
	return value, nil
}
func validateSub2APIResourceShape(raw []byte, m sub2apiManifest, r sub2apiCallRequest) error {
	var d map[string]json.RawMessage
	if decodeSub2API(raw, &d) != nil || string(d["schemaVersion"]) != "1" {
		return errSub2API
	}
	var kind, target string
	if json.Unmarshal(d["materialKind"], &kind) != nil || json.Unmarshal(d["target"], &target) != nil || kind != m.MaterialKind || target != r.Target {
		return errSub2API
	}
	shapes := map[string]string{
		"identity": "objectId tenantId serverId daemonId endpoint project", "postgres": "systemIdentifier user maintenanceDatabase databases roles toolPath version catalogDigest",
		"redis": "runId version", "paths": "app rdb acl controlled runtime env", "roots": "backup temporary log metrics coordination", "locks": "postgres redis volumes",
	}
	for key, keys := range shapes {
		if _, err := sub2apiJSONFields(d[key], keys); err != nil {
			return err
		}
	}
	if validateSub2APIDatabaseDocument(d["postgres"], d["redis"]) != nil {
		return errSub2API
	}
	var identity map[string]string
	if json.Unmarshal(d["identity"], &identity) != nil || identity["objectId"] != m.Resources["container"].ObjectID || identity["tenantId"] != m.Resources["container"].TenantID || identity["serverId"] != m.Resources["container"].ServerID || !strings.HasPrefix(identity["endpoint"], "unix:///") || identity["project"] != m.Runtime.ProjectName || !sub2apiPlain(identity["daemonId"]) {
		return errSub2API
	}
	paths, _ := sub2apiJSONFields(d["paths"], shapes["paths"])
	var resources map[string]struct {
		Selector string `json:"selector"`
	}
	if json.Unmarshal(d["resources"], &resources) != nil {
		return errSub2API
	}
	for key, entry := range paths {
		var source struct {
			Path string `json:"path"`
			UID  int    `json:"uid"`
			GID  int    `json:"gid"`
		}
		if decodeSub2APIBackup(entry, &source) != nil || source.Path != resources[key].Selector || source.UID < 0 || source.GID < 0 {
			return errSub2API
		}
	}
	var roots map[string]string
	if json.Unmarshal(d["roots"], &roots) != nil {
		return errSub2API
	}
	for key, path := range roots {
		if path != resources[key].Selector {
			return errSub2API
		}
	}
	var locks map[string]struct {
		Path   string `json:"path"`
		Device uint64 `json:"device"`
		Inode  uint64 `json:"inode"`
	}
	if decodeSub2APIBackup(d["locks"], &locks) != nil {
		return errSub2API
	}
	for key, lock := range locks {
		if lock.Path != resources[key+"-lock"].Selector || lock.Inode == 0 {
			return errSub2API
		}
	}
	return nil
}
