package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// 仅测试装配；普通启动没有任何 observe/inspect/run/commitReceipt 的真实实现。
type syntheticSub2API struct {
	t                        *testing.T
	m                        sub2apiManifest
	digest, journal          string
	state                    sub2apiState
	beforeBytes, targetBytes []byte
	calls                    []sub2apiCallRequest
	observeHook              func(*sub2apiObservation)
	completeHook             func(*sub2apiCompletion)
	saveHook                 func([]byte) ([]byte, error)
	fail, partial            string
}

func (b *syntheticSub2API) observe(_ context.Context, m sub2apiManifest) (sub2apiObservation, error) {
	o := sub2apiObservation{ManifestDigest: b.digest, ImplementationDigest: sub2apiDigest(m.Files), EnvironmentDigest: sub2apiDigest(m.Environment),
		Resources: m.Resources, State: b.state, ImmutableInputs: true, ClosedDependencies: true, IsolatedEnvironment: true, ExclusiveResources: true}
	if b.observeHook != nil {
		b.observeHook(&o)
	}
	return o, nil
}
func (b *syntheticSub2API) inspect(ctx context.Context, r sub2apiCallRequest) (sub2apiCompletion, error) {
	return b.complete(ctx, r)
}
func (b *syntheticSub2API) runSub2API(ctx context.Context, r sub2apiCallRequest) (sub2apiCompletion, error) {
	return b.complete(ctx, r)
}

func (b *syntheticSub2API) complete(_ context.Context, r sub2apiCallRequest) (sub2apiCompletion, error) {
	b.calls = append(b.calls, r)
	outcome := "success"
	if r.Phase == b.fail {
		outcome = "failed"
	}
	if r.Phase == "apply" {
		if outcome == "success" || b.partial == "target" {
			b.state = b.m.TargetState
		}
		if b.partial == "controlled" || b.partial == "pair" {
			b.state.Controlled = b.m.TargetState.Controlled
		}
		if b.partial == "pair" {
			b.state.Runtime = b.m.TargetState.Runtime
		}
	}
	if r.Phase == "rollback" {
		migration := b.state.Migration
		b.state = b.m.Before
		b.state.Migration = migration
	}
	for path, digest := range map[string]string{b.m.Runtime.ControlledCompose: b.state.Controlled, b.m.Runtime.RuntimeCompose: b.state.Runtime} {
		data := b.beforeBytes
		if digest == b.m.TargetState.Controlled {
			data = b.targetBytes
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			b.t.Fatal(err)
		}
	}
	identities := sub2apiFactIdentities(b.m, r)
	fact := func(role string) sub2apiFact {
		return sub2apiFact{Status: "complete", Identity: identities[role], Proof: model.WorkDigest("synthetic proof:" + r.CallID + role)}
	}
	c := sub2apiCompletion{Request: r, Outcome: outcome, After: b.state, Facts: sub2apiFacts{fact("host"), fact("daemon"), fact("migration"), fact("bgsave"), fact("successors"), fact("cleanup")}}
	if r.Phase == "backup" && c.Outcome == "success" {
		b.backup(&c)
	}
	if b.completeHook != nil {
		b.completeHook(&c)
	}
	return c, nil
}

func (b *syntheticSub2API) commitReceipt(ctx context.Context, id string, raw []byte) ([]byte, error) {
	if b.saveHook != nil {
		return b.saveHook(raw)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	f, err := os.CreateTemp(b.journal, ".receipt-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	path := filepath.Join(b.journal, id+".json")
	// link 原子发布且拒绝已存在的 callId；不会覆盖旧回执。
	if err = os.Link(f.Name(), path); err != nil {
		return nil, err
	}
	if err = os.Remove(f.Name()); err != nil {
		return nil, err
	}
	d, err := os.Open(b.journal)
	if err != nil {
		return nil, err
	}
	err = d.Sync()
	_ = d.Close()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

type sub2apiFixture struct {
	p       *sub2apiExecutionProfile
	b       *syntheticSub2API
	service model.ServiceDefinition
	action  model.ActionDefinition
	call    *releaseCallContext
	root    string
}

func newSub2APIFixture(t *testing.T) *sub2apiFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := &syntheticSub2API{t: t, beforeBytes: []byte("synthetic compose: before\n"), targetBytes: []byte("synthetic compose: target\n"), journal: filepath.Join(root, "receipts")}
	m := sub2apiManifest{Version: 1, Profile: sub2apiProfileID, Target: "v1.1.0", MaterialKind: "synthetic", BundleRoot: filepath.Join(root, "bundle"), BackupMode: "service-exclusive-v1"}
	m.Resources = make(map[string]sub2apiResource)
	for _, role := range sub2apiResourceRoles {
		m.Resources[role] = sub2apiResource{Selector: "synthetic:" + role, ObjectID: "service:sub2api", TenantID: "team", ServerID: "local"}
	}
	set := func(role, value string) { r := m.Resources[role]; r.Selector = value; m.Resources[role] = r }
	for _, role := range sub2apiPathRoles {
		set(role, filepath.Join(root, "sub2api", role))
	}
	for _, role := range []string{"postgres-lock", "redis-lock", "volumes-lock", "coordination-root"} {
		r := m.Resources[role]
		r.ObjectID = "coordination:backup"
		if role != "coordination-root" {
			r.Selector = filepath.Join(root, "locks", "ops-backup-"+strings.TrimSuffix(role, "-lock")+".lock")
		}
		m.Resources[role] = r
	}
	set("operation-root", filepath.Join(root, "operations"))
	set("inspection-root", root)
	m.Prepared = filepath.Join(m.BundleRoot, "evidence/prepared.json")
	m.Source = filepath.Join(m.BundleRoot, "evidence/source.json")
	m.Rehearsal = filepath.Join(m.BundleRoot, "evidence/rehearsal.json")
	set("prepared", filepath.Dir(m.Prepared))
	set("source", m.Source)
	set("rehearsal", m.Rehearsal)
	set("service", "sub2api")
	m.Runtime = model.ComposeServiceRuntime{ControlledCompose: m.Resources["controlled-compose"].Selector, RuntimeCompose: m.Resources["runtime-compose"].Selector,
		EnvFile: m.Resources["environment"].Selector, ProjectName: m.Resources["project"].Selector, ApplicationService: "sub2api", ApplicationContainer: m.Resources["container"].Selector,
		DependencyContainers: []string{m.Resources["postgres"].Selector, m.Resources["redis"].Selector}, HealthURL: m.Resources["health-endpoint"].Selector,
		ReleaseCatalog: m.Resources["catalog"].Selector, PreparedReleaseDir: filepath.Dir(m.Prepared), InspectExecutable: filepath.Join(m.BundleRoot, sub2apiWrapper), UpdateExecutable: filepath.Join(m.BundleRoot, sub2apiWrapper)}
	m.Environment = map[string]string{"LANG": "C", "LC_ALL": "C", "TZ": "UTC", "PATH": filepath.Join(m.BundleRoot, "tools")}
	env := []byte("SYNTHETIC_ONLY=1\n")
	m.EnvironmentFileDigest = model.WorkDigest(string(env))
	m.Before = sub2apiState{Controlled: model.WorkDigest(string(b.beforeBytes)), Runtime: model.WorkDigest(string(b.beforeBytes)), Image: "synthetic:old", ImageID: "sha256:" + model.WorkDigest("old-image"), Version: "1.0.0", Commit: strings.Repeat("a", 40), Migration: model.WorkDigest("baseline-set")}
	m.TargetState = sub2apiState{Controlled: model.WorkDigest(string(b.targetBytes)), Runtime: model.WorkDigest(string(b.targetBytes)), Image: "synthetic:new", ImageID: "sha256:" + model.WorkDigest("new-image"), Version: "1.1.0", Commit: strings.Repeat("b", 40), Migration: model.WorkDigest("target-set")}
	m.SourceDigest = model.WorkDigest("synthetic offline source")
	m.BeforeImageBundleDigest = model.WorkDigest("synthetic old-image archive")
	m.TargetImageBundleDigest = model.WorkDigest("synthetic new-image archive")
	evidence := sub2apiMaterial{BeforeImageBundleDigest: m.BeforeImageBundleDigest, TargetImageBundleDigest: m.TargetImageBundleDigest, GitCommit: m.TargetState.Commit, Kind: "synthetic", Target: m.Target, Status: "prepared", SourceDigest: m.SourceDigest, BeforeImageID: m.Before.ImageID, TargetImageID: m.TargetState.ImageID, BeforeMigration: m.Before.Migration, TargetMigration: m.TargetState.Migration, RollbackCompatible: true}
	m.Alerts = sub2apiAlertScope{Endpoint: "http://synthetic.invalid", Matchers: map[string]string{"service": "sub2api", "tenantId": "team", "serverId": "local", "objectId": "service:sub2api"}, BlockingAlerts: []string{"SyntheticFailure"}, MaintenanceAlerts: []string{"SyntheticFailure"}}
	set("alertmanager", sub2apiDigest(m.Alerts))
	set("nginx", sub2apiDigest(m.Nginx))
	m.Files = make(map[string]sub2apiFile)
	for name, imports := range sub2apiDependencyGraph() {
		data := []byte("synthetic inert bundle entry: " + name)
		switch name {
		case "evidence/source.bundle":
			data = []byte("synthetic offline source")
		case "evidence/before-image.bundle":
			data = []byte("synthetic old-image archive")
		case "evidence/target-image.bundle":
			data = []byte("synthetic new-image archive")
		}
		if filepath.Ext(name) == ".json" {
			data, _ = json.Marshal(evidence)
		}
		if name == "scripts/backup/backup-control.json" {
			data, _ = json.Marshal(map[string]any{"schemaVersion": 1, "mode": "coordinated", "coordinationRoot": m.Resources["coordination-root"].Selector, "instanceDigest": model.WorkDigest("synthetic instance")})
		}
		path := filepath.Join(m.BundleRoot, name)
		writeSub2APIFile(t, path, data)
		m.Files[name] = sub2apiFile{Digest: model.WorkDigest(string(data)), Imports: imports}
	}
	writeSub2APIFile(t, m.Runtime.ControlledCompose, b.beforeBytes)
	writeSub2APIFile(t, m.Runtime.RuntimeCompose, b.beforeBytes)
	writeSub2APIFile(t, m.Runtime.EnvFile, env)
	if err = os.MkdirAll(b.journal, 0700); err != nil {
		t.Fatal(err)
	}
	s := model.ServiceDefinition{Name: "sub2api", ObjectID: "service:sub2api", TenantID: "team", ServerID: "local", Adapter: filepath.Join(m.BundleRoot, sub2apiCompose), AdapterContractVersion: 2, Runtime: &m.Runtime}
	s.RecoveryPointPolicy = &model.RecoveryPointPolicy{RequiredArtifactRoles: []string{"postgres-sub2api", "redis", "volume-sub2api-data", "configs", "runtime-snapshot"}, RecoverableSeconds: 3600}
	s.AlertPolicy = model.AlertPolicyDefinition{Matchers: m.Alerts.Matchers, BlockingAlerts: m.Alerts.BlockingAlerts, MaintenanceAlerts: m.Alerts.MaintenanceAlerts}
	s.Metadata.Type = "service"
	a := model.ActionDefinition{Name: "update", Steps: append([]string(nil), sub2apiPhases...)}
	s.Actions = map[string]model.ActionDefinition{"update": a}
	p := &sub2apiInspectionProfile{manifestPath: filepath.Join(root, "manifest.json"), backend: b}
	x := &sub2apiFixture{p: &sub2apiExecutionProfile{sub2apiInspectionProfile: p, execution: b}, b: b, service: s, action: a, root: root}
	b.m = m
	b.state = m.Before
	x.saveManifest(t)
	x.call = &releaseCallContext{preparationID: mustUUID(t), planID: mustUUID(t), taskID: mustUUID(t), leaseID: mustUUID(t), target: m.Target, scopeDigest: model.WorkDigest("synthetic approved scope")}
	return x
}

func writeSub2APIFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func (x *sub2apiFixture) saveManifest(t *testing.T) {
	t.Helper()
	raw, _ := json.Marshal(x.b.m)
	writeSub2APIFile(t, x.p.manifestPath, raw)
	x.p.manifestDigest = model.WorkDigest(string(raw))
	x.b.digest = x.p.manifestDigest
	scope := x.b.m.scope(x.p.manifestPath, x.p.manifestDigest)
	x.service.ReleaseScope = &scope
	copy := x.b.m.Runtime
	x.service.Runtime = &copy
}
func (x *sub2apiFixture) input(phase string) ExecuteInput {
	return ExecuteInput{Service: x.service, Action: "update", Phase: phase, Target: x.b.m.Target, AdapterKind: adapterKindService,
		OperationDir: filepath.Join(x.b.m.Resources["operation-root"].Selector, x.call.taskID), releaseContext: x.call}
}
func (x *sub2apiFixture) run(t *testing.T, phase string) inspectionCall {
	t.Helper()
	c := x.p.executeReleaseCall(context.Background(), x.input(phase))
	if c.Settled == nil {
		t.Fatalf("%s missing settlement: %v", phase, c.Err)
	}
	return c
}
