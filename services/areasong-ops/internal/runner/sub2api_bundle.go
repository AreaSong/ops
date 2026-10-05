package runner

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const sub2apiCompose = "services/areasong-ops/adapters/compose-service.sh"
const sub2apiWrapper = "services/areasong-ops/adapters/sub2api.sh"
const sub2apiLegacy = "scripts/deploy/update-control/adapters/sub2api.sh"

// 封闭依赖图只是待验证合同，不授信这些路径的现存脚本行为。
func sub2apiDependencyGraph() map[string][]string {
	b := "scripts/backup/"
	g := map[string][]string{
		sub2apiCompose: {sub2apiWrapper, b + "sub2api_backup_contract.py"}, sub2apiWrapper: {sub2apiLegacy, b + "restore_point_metadata.py", b + "sub2api_backup_contract.py"},
		sub2apiLegacy:            {b + "backup-postgres.sh", b + "backup-redis.sh", b + "backup-volumes.sh", b + "run-backup-job.sh", b + "sub2api_backup_contract.py"},
		b + "backup-postgres.sh": {b + "run-backup-job.sh"}, b + "backup-redis.sh": {b + "run-backup-job.sh", b + "restore_env.py"},
		b + "backup-volumes.sh": {b + "run-backup-job.sh", b + "restore_env.py", b + "areasong_ops_snapshot.py"}, b + "run-backup-job.sh": {b + "sub2api_backup.py", b + "sub2api_backup_contract.py", b + "backup-control.json"},
		b + "restore_point_metadata.py": {b + "backup_manifest.py", b + "restore_contract.py", b + "sub2api_backup.py", b + "sub2api_backup_contract.py", b + "sub2api_backup_archive.py"},
		b + "backup_manifest.py":        {}, b + "restore_contract.py": {b + "restore_point_metadata.py", b + "sub2api_backup_archive.py"}, b + "restore_env.py": {},
		"evidence/prepared.json": {}, "evidence/source.json": {}, "evidence/rehearsal.json": {},
	}
	g[b+"sub2api_backup.py"] = []string{b + "sub2api_backup_contract.py", b + "sub2api_backup_archive.py", b + "restore_point_metadata.py"}
	g[b+"sub2api_backup_contract.py"] = []string{b + "sub2api_backup_archive.py", b + "backup-control.json"}
	g[b+"sub2api_backup_archive.py"] = []string{}
	g[b+"backup-control.json"] = []string{}
	for _, name := range []string{"backup-postgres.sh", "backup-redis.sh", "backup-volumes.sh"} {
		g[b+name] = append(g[b+name], b+"sub2api_backup_contract.py")
	}
	for _, tool := range strings.Fields("bash docker jq python3 curl sha256sum awk openssl stat date timeout flock install tar gzip runtime nice mktemp mkdir cp mv rm dirname basename grep tail head cat cmp seq sleep tr sed sort find xargs tee touch chmod chown id env sync hostname nginx ss systemctl psql pg_dumpall redis-cli") {
		g["tools/"+tool] = []string{}
	}
	g["evidence/source.bundle"] = []string{}
	g["evidence/before-image.bundle"] = []string{}
	g["evidence/target-image.bundle"] = []string{}
	g[b+"areasong_ops_snapshot.py"] = []string{}
	g["services/areasong-ops/adapters/nginx-traffic.sh"] = []string{}
	return g
}

type sub2apiMaterial struct {
	BeforeImageBundleDigest, TargetImageBundleDigest                                                   string
	GitCommit                                                                                          string
	Kind, Target, Status, SourceDigest, BeforeImageID, TargetImageID, BeforeMigration, TargetMigration string
	RollbackCompatible                                                                                 bool
}

func (m sub2apiManifest) validateBundle() error {
	if !model.ValidWorkDigest(m.SourceDigest) || m.SourceDigest != m.Files["evidence/source.bundle"].Digest ||
		!model.ValidWorkDigest(m.BeforeImageBundleDigest) || !model.ValidWorkDigest(m.TargetImageBundleDigest) ||
		m.BeforeImageBundleDigest != m.Files["evidence/before-image.bundle"].Digest || m.TargetImageBundleDigest != m.Files["evidence/target-image.bundle"].Digest {
		return errSub2API
	}
	graph := sub2apiDependencyGraph()
	if len(m.Files) != len(graph) {
		return errSub2API
	}
	for name, imports := range graph {
		file, ok := m.Files[name]
		if !ok || !reflect.DeepEqual(file.Imports, imports) || !model.ValidWorkDigest(file.Digest) {
			return errSub2API
		}
		digest, err := sub2apiFileDigest(filepath.Join(m.BundleRoot, name))
		if err != nil || digest != file.Digest {
			return errSub2API
		}
	}
	// 不允许未声明 helper、导入文件或可变配置混入实现根。
	err := filepath.WalkDir(m.BundleRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.Type()&os.ModeSymlink != 0 {
			return errSub2API
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(m.BundleRoot, path)
		if err != nil {
			return errSub2API
		}
		if _, ok := graph[rel]; !ok {
			return errSub2API
		}
		return nil
	})
	if err != nil {
		return errSub2API
	}
	for _, name := range []string{"prepared", "source", "rehearsal"} {
		path := filepath.Join(m.BundleRoot, "evidence", name+".json")
		b, err := sub2apiRegular(path)
		var evidence sub2apiMaterial
		if err != nil || decodeSub2API(b, &evidence) != nil || evidence.Kind != m.MaterialKind || evidence.Target != m.Target ||
			evidence.Status != "prepared" || evidence.SourceDigest != m.SourceDigest || evidence.GitCommit != m.TargetState.Commit ||
			evidence.BeforeImageID != m.Before.ImageID || evidence.TargetImageID != m.TargetState.ImageID ||
			evidence.BeforeImageBundleDigest != m.BeforeImageBundleDigest || evidence.TargetImageBundleDigest != m.TargetImageBundleDigest ||
			evidence.BeforeMigration != m.Before.Migration || evidence.TargetMigration != m.TargetState.Migration || !evidence.RollbackCompatible {
			return errSub2API
		}
	}
	if m.Prepared != filepath.Join(m.BundleRoot, "evidence/prepared.json") || m.Source != filepath.Join(m.BundleRoot, "evidence/source.json") ||
		m.Rehearsal != filepath.Join(m.BundleRoot, "evidence/rehearsal.json") {
		return errSub2API
	}
	return nil
}

func (m sub2apiManifest) scope(path, digest string) model.ReleaseScopeDefinition {
	s := model.ReleaseScopeDefinition{Version: 1, Mode: "local", ProfileID: sub2apiProfileID, ImplementationDigests: map[string]string{path: digest}}
	for name, f := range m.Files {
		s.ImplementationDigests[filepath.Join(m.BundleRoot, name)] = f.Digest
	}
	keys := append([]string(nil), sub2apiResourceRoles...)
	sort.Strings(keys)
	for _, role := range keys {
		r := m.Resources[role]
		s.Resources = append(s.Resources, model.ReleaseScopeResource{Kind: role, Selector: r.Selector, ObjectID: r.ObjectID})
	}
	return s
}
