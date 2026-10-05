package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// 仅合成协议材料；这些字节不构成 PostgreSQL/Redis 可恢复性证据。
func (b *syntheticSub2API) backup(c *sub2apiCompletion) {
	r := c.Request
	runtime := sub2apiRuntimeV2{SchemaVersion: 2, Service: "sub2api", Containers: map[string]sub2apiRuntimeContainer{}}
	runtime.Database.User = "sub2api"
	runtime.Database.Database = "sub2api"
	runtime.Database.Migrations = 23
	for _, role := range []string{"app", "postgres", "redis"} {
		runtime.Containers[role] = sub2apiRuntimeContainer{Name: "sub2api-" + role, ConfiguredImage: b.m.Before.Image, ImageID: b.m.Before.ImageID, ContainerID: model.WorkDigest("synthetic:" + role), Version: b.m.Before.Version, Revision: b.m.Before.Commit}
	}
	mapping := map[string]string{"app": "app-mount", "rdb": "redis-mount", "acl": "redis-mount", "controlled": "controlled-compose", "runtime": "runtime-compose", "env": "environment", "backup": "backup-root", "temporary": "temporary-root", "log": "log", "metrics": "metrics", "coordination": "coordination-root", "postgres-lock": "postgres-lock", "redis-lock": "redis-lock", "volumes-lock": "volumes-lock", "operation": "operation-root"}
	resources := map[string]any{}
	for key, role := range mapping {
		v := b.m.Resources[role]
		path := v.Selector
		action := "write"
		if key == "rdb" {
			path = filepath.Join(path, "dump.rdb")
		}
		if key == "acl" {
			path = filepath.Join(path, "users.acl")
		}
		if key == "operation" {
			path = r.OperationDir
		}
		if key == "app" || key == "rdb" || key == "acl" || key == "controlled" || key == "runtime" || key == "env" {
			action = "read"
		}
		if strings.HasSuffix(key, "-lock") {
			action = "coordinate"
		}
		resources[key] = map[string]any{"selector": path, "objectId": v.ObjectID, "tenantId": v.TenantID, "serverId": v.ServerID, "actions": []string{action}}
	}
	containers := map[string]any{}
	for role, container := range runtime.Containers {
		containers[role] = map[string]string{"id": container.ContainerID, "generation": "synthetic-generation"}
	}
	paths := map[string]any{}
	roots := map[string]string{}
	locks := map[string]any{}
	for _, key := range []string{"app", "rdb", "acl", "controlled", "runtime", "env"} {
		paths[key] = map[string]any{"path": resources[key].(map[string]any)["selector"], "uid": os.Geteuid(), "gid": os.Getegid()}
	}
	for _, key := range []string{"backup", "temporary", "log", "metrics", "coordination"} {
		roots[key] = resources[key].(map[string]any)["selector"].(string)
	}
	for _, key := range []string{"postgres", "redis", "volumes"} {
		locks[key] = map[string]any{"path": resources[key+"-lock"].(map[string]any)["selector"], "device": 1, "inode": 1}
	}
	resourceRaw, _ := json.Marshal(map[string]any{"schemaVersion": 1, "materialKind": "synthetic", "target": r.Target,
		"identity":   map[string]string{"objectId": "service:sub2api", "tenantId": "team", "serverId": "local", "daemonId": "synthetic-daemon", "endpoint": "unix:///synthetic/no-daemon.sock", "project": b.m.Runtime.ProjectName},
		"containers": containers, "postgres": map[string]any{"systemIdentifier": "123", "user": "sub2api", "maintenanceDatabase": "postgres", "databases": []string{"postgres", "template0", "template1", "sub2api"}, "roles": []string{"sub2api"}, "toolPath": "/usr/local/bin:/usr/bin:/bin", "version": "18.1", "catalogDigest": model.WorkDigest("synthetic catalog")},
		"redis": map[string]string{"runId": "synthetic-redis", "version": "8.0"}, "paths": paths, "roots": roots, "locks": locks,
		"resources": resources, "runtime": sub2apiRuntimeBaseline{runtime.Containers, runtime.Database}})
	resourcePath := filepath.Join(r.OperationDir, "backup-resources.json")
	writeSub2APIFile(b.t, resourcePath, resourceRaw)
	request := sub2apiBackupRequest{ProtocolVersion: 1, Mode: "service-exclusive-v1", Service: "sub2api", Job: "sub2api-set", ResourceManifest: sub2apiBackupReference{resourcePath, model.WorkDigest(string(resourceRaw))}, Deadline: time.Now().Add(time.Minute).Unix()}
	request.sub2apiBackupBinding = sub2apiBackupExpected(r, request.ResourceManifest.SHA256)
	requestRaw, _ := json.Marshal(request)
	writeSub2APIFile(b.t, filepath.Join(r.OperationDir, "backup-request.json"), requestRaw)
	checksum := model.WorkDigest(string(requestRaw))
	proof := model.WorkDigest("synthetic backup proof:" + r.CallID)
	root := filepath.Join(b.m.Resources["backup-root"].Selector, r.TaskID, r.CallID)
	point := &model.RecoveryPointEvidence{SchemaVersion: 1, Service: "sub2api", TaskID: r.TaskID, TenantID: "team", ServerID: "local", CreatedAt: time.Now().UTC()}
	set := sub2apiBackupSet{sub2apiBackupBinding: request.sub2apiBackupBinding, ProtocolVersion: 1, Service: "sub2api", Job: "sub2api-set", RequestDigest: checksum, State: "completed", StartedAt: time.Now().Unix(), EndedAt: time.Now().Unix(), Proof: proof, Cleanup: "retained", Category: "none"}

	for i, f := range sub2apiBackupFormats {
		data := []byte("synthetic protocol fixture: " + f[0])
		if i == 4 {
			runtime.Backup = sub2apiRuntimeBackup{sub2apiBackupBinding: request.sub2apiBackupBinding, ProtocolVersion: 1, RequestDigest: checksum, Artifacts: set.Artifacts, Subreceipts: set.Subreceipts, Proof: proof, Consistency: "synthetic-coordinated"}
			data, _ = json.Marshal(runtime)
		}
		path := filepath.Join(root, f[1])
		writeSub2APIFile(b.t, path, data)
		item := sub2apiBackupArtifact{f[0], f[1], f[2], int64(len(data)), "sha256:" + model.WorkDigest(string(data))}
		set.Artifacts = append(set.Artifacts, item)
		point.Artifacts = append(point.Artifacts, model.RecoveryArtifact{Role: f[0], Path: path, SizeBytes: item.SizeBytes, SHA256: item.SHA256})
		if i < 4 {
			job := sub2apiBackupJobs[i]
			child := sub2apiBackupCollection{request.sub2apiBackupBinding, 1, "sub2api", job, checksum, "completed", item, proof}
			childRaw, _ := json.Marshal(child)
			writeSub2APIFile(b.t, filepath.Join(root, job+".receipt.json"), childRaw)
			set.Subreceipts = append(set.Subreceipts, sub2apiBackupChild{job, job + ".receipt.json", "sha256:" + model.WorkDigest(string(childRaw))})
		}
	}
	raw, _ := json.Marshal(set)
	writeSub2APIFile(b.t, filepath.Join(r.OperationDir, "backup-result.json"), raw)
	c.BackupReceiptDigest = model.WorkDigest(string(raw))
	c.BackupResourceDigest = request.ResourceDigest
	c.BackupSource = sub2apiRuntimeBaseline{runtime.Containers, runtime.Database}
	c.RecoveryPoint = point
}

func TestSub2APIBackupReceiptRejectsTampering(t *testing.T) {
	for _, mode := range []string{"missing", "cross-call", "runtime-v1", "public-mode", "extra-role", "alias-field", "raw-secret", "truncated", "runtime-source", "resource-source", "operation-sibling"} {
		t.Run(mode, func(t *testing.T) {
			x := newSub2APIFixture(t)
			x.run(t, "preflight")
			x.b.completeHook = func(c *sub2apiCompletion) {
				if c.Request.Phase != "backup" {
					return
				}
				path := filepath.Join(c.Request.OperationDir, "backup-result.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "missing":
					c.BackupReceiptDigest = ""
				case "cross-call":
					raw = []byte(strings.ReplaceAll(string(raw), c.Request.CallID, mustUUID(t)))
				case "runtime-v1", "runtime-source":
					p := filepath.Join(x.b.m.Resources["backup-root"].Selector, c.Request.TaskID, c.Request.CallID, "runtime.json")
					content, _ := os.ReadFile(p)
					if mode == "runtime-v1" {
						content = []byte(strings.Replace(string(content), `"schemaVersion":2`, `"schemaVersion":1`, 1))
					} else {
						var snapshot sub2apiRuntimeV2
						_ = json.Unmarshal(content, &snapshot)
						value := snapshot.Containers["app"]
						value.ContainerID = model.WorkDigest("another instance")
						snapshot.Containers["app"] = value
						content, _ = json.Marshal(snapshot)
					}
					writeSub2APIFile(t, p, content)
					var result sub2apiBackupSet
					_ = json.Unmarshal(raw, &result)
					result.Artifacts[4].SizeBytes = int64(len(content))
					result.Artifacts[4].SHA256 = "sha256:" + model.WorkDigest(string(content))
					c.RecoveryPoint.Artifacts[4].SizeBytes = int64(len(content))
					c.RecoveryPoint.Artifacts[4].SHA256 = result.Artifacts[4].SHA256
					raw, _ = json.Marshal(result)
				case "resource-source":
					c.BackupResourceDigest = model.WorkDigest("other observation")
				case "operation-sibling":
					resourcePath := filepath.Join(c.Request.OperationDir, "backup-resources.json")
					content, _ := os.ReadFile(resourcePath)
					var document map[string]any
					_ = json.Unmarshal(content, &document)
					document["resources"].(map[string]any)["operation"].(map[string]any)["selector"] = filepath.Join(filepath.Dir(c.Request.OperationDir), mustUUID(t))
					content, _ = json.Marshal(document)
					if validateSub2APIBackupResources(x.b.m, c.Request, content) == nil {
						t.Fatal("sibling operation accepted")
					}
					c.BackupResourceDigest = model.WorkDigest("rejected sibling")
				case "public-mode":
					if err := os.Chmod(path, 0644); err != nil {
						t.Fatal(err)
					}
				case "extra-role":
					c.RecoveryPoint.Artifacts = append(c.RecoveryPoint.Artifacts, c.RecoveryPoint.Artifacts[0])
				case "alias-field":
					raw = []byte(strings.Replace(string(raw), `"callId":`, `"CallId":`, 1))
				case "raw-secret":
					raw = append(raw[:len(raw)-1], []byte(`,"password":"SYNTHETIC-SECRET"}`)...)
				case "truncated":
					raw = raw[:len(raw)/2]
				}
				if mode != "missing" {
					writeSub2APIFile(t, path, raw)
					c.BackupReceiptDigest = model.WorkDigest(string(raw))
				}
			}
			result := x.p.executeReleaseCall(context.Background(), x.input("backup"))
			if result.Settled != nil || result.Err == nil {
				t.Fatal("invalid scoped backup settled")
			}
		})
	}
}

func TestSub2APIBackupDatabaseResourceTypes(t *testing.T) {
	validPG := `{"systemIdentifier":"123","user":"sub2api","maintenanceDatabase":"postgres","databases":["postgres","sub2api"],"roles":["sub2api"],"toolPath":"/usr/local/bin:/usr/bin:/bin","version":"18.1","catalogDigest":"` + strings.Repeat("a", 64) + `"}`
	validRedis := `{"runId":"synthetic-run","version":"8.0"}`
	if validateSub2APIDatabaseDocument([]byte(validPG), []byte(validRedis)) != nil {
		t.Fatal("valid synthetic catalog rejected")
	}
	for _, input := range []string{strings.Replace(validPG, `["postgres","sub2api"]`, `"postgres"`, 1), strings.Replace(validPG, `"roles":["sub2api"]`, `"roles":["sub2api","postgres"]`, 1)} {
		if validateSub2APIDatabaseDocument([]byte(input), []byte(validRedis)) == nil {
			t.Fatal("invalid PostgreSQL catalog accepted")
		}
	}
	if validateSub2APIDatabaseDocument([]byte(validPG), []byte(`{"runId":null,"version":"8.0"}`)) == nil {
		t.Fatal("null Redis identity accepted")
	}
}

func TestSub2APIBackupRequiresFiveRoleConsumerPolicy(t *testing.T) {
	x := newSub2APIFixture(t)
	x.service.RecoveryPointPolicy.RequiredArtifactRoles = []string{"postgres-sub2api", "redis", "volume-sub2api-data"}
	if x.b.m.validate(x.service, x.b.m.Target) == nil {
		t.Fatal("private backup accepted incompatible three-role restore consumer")
	}
}
