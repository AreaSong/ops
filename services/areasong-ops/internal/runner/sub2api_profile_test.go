package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestSub2APIFixedScopeAndCapabilities(t *testing.T) {
	x := newSub2APIFixture(t)
	s, err := x.p.resolvePlanInspectionScope(context.Background(), x.service, x.action, releaseScopeInput{target: x.b.m.Target})
	if err != nil || !reflect.DeepEqual(s, *x.service.ReleaseScope) {
		t.Fatal(err)
	}
	if _, ok := any(x.p.sub2apiInspectionProfile).(releaseExecutor); ok {
		t.Fatal("inspection grants execution")
	}
	if _, ok := any(CommandExecutor{}).(planInspectionExecutor); ok {
		t.Fatal("command grants inspection")
	}
	if _, ok := any(CommandExecutor{}).(releaseExecutor); ok {
		t.Fatal("command grants execution")
	}
	if _, err = x.p.Execute(context.Background(), x.input("preflight")); err == nil {
		t.Fatal("plain entry accepted")
	}
	var empty sub2apiInspectionProfile
	if c := empty.executePlanInspection(context.Background(), ExecuteInput{}); c.Err == nil || c.Settled != nil {
		t.Fatal("default real inspection")
	}
	noExecution := &sub2apiExecutionProfile{sub2apiInspectionProfile: x.p.sub2apiInspectionProfile}
	if c := noExecution.executeReleaseCall(context.Background(), x.input("preflight")); c.Err == nil || c.Settled != nil {
		t.Fatal("default real execution")
	}
	if sub2apiHostExecutionError() == nil {
		t.Fatal("platform is not a capability")
	}
}

func TestSub2APIManifestRejectsUnprovenScope(t *testing.T) {
	cases := map[string]func(*sub2apiFixture){
		"missing resource": func(x *sub2apiFixture) { delete(x.b.m.Resources, "database") },
		"extra resource":   func(x *sub2apiFixture) { x.b.m.Resources["other"] = x.b.m.Resources["database"] },
		"missing owner": func(x *sub2apiFixture) {
			r := x.b.m.Resources["database"]
			r.ObjectID = ""
			x.b.m.Resources["database"] = r
		},
		"wrong tenant": func(x *sub2apiFixture) {
			r := x.b.m.Resources["redis"]
			r.TenantID = "other"
			x.b.m.Resources["redis"] = r
		},
		"wrong server": func(x *sub2apiFixture) {
			r := x.b.m.Resources["daemon"]
			r.ServerID = "other"
			x.b.m.Resources["daemon"] = r
		},
		"shared backup": func(x *sub2apiFixture) { x.b.m.BackupMode = "shared" },
		"shared backup root": func(x *sub2apiFixture) {
			r := x.b.m.Resources["backup-root"]
			r.Selector = "/var/backups/ops"
			x.b.m.Resources["backup-root"] = r
		},
		"env override":      func(x *sub2apiFixture) { x.b.m.Environment["DOCKER_HOST"] = "bad" },
		"tool override":     func(x *sub2apiFixture) { x.b.m.Environment["PATH"] = "/usr/bin" },
		"profile":           func(x *sub2apiFixture) { x.b.m.Profile = "other" },
		"missing prepared":  func(x *sub2apiFixture) { x.b.m.Prepared = "" },
		"missing image":     func(x *sub2apiFixture) { x.b.m.TargetState.ImageID = "" },
		"missing source":    func(x *sub2apiFixture) { x.b.m.Source = "" },
		"missing rehearsal": func(x *sub2apiFixture) { x.b.m.Rehearsal = "" },
		"helper import": func(x *sub2apiFixture) {
			f := x.b.m.Files[sub2apiLegacy]
			f.Imports = append(f.Imports, "unknown.sh")
			x.b.m.Files[sub2apiLegacy] = f
		},
		"escape":             func(x *sub2apiFixture) { x.b.m.Files["../escape"] = sub2apiFile{Digest: model.WorkDigest("bad")} },
		"no immutable fence": func(x *sub2apiFixture) { x.b.observeHook = func(o *sub2apiObservation) { o.ImmutableInputs = false } },
		"undeclared inputs": func(x *sub2apiFixture) {
			x.b.observeHook = func(o *sub2apiObservation) { o.ClosedDependencies = false }
		},
		"inherited env": func(x *sub2apiFixture) {
			x.b.observeHook = func(o *sub2apiObservation) { o.IsolatedEnvironment = false }
		},
		"shared instance": func(x *sub2apiFixture) {
			x.b.observeHook = func(o *sub2apiObservation) { o.ExclusiveResources = false }
		},
		"observed omitted resource": func(x *sub2apiFixture) {
			x.b.observeHook = func(o *sub2apiObservation) { o.Resources = map[string]sub2apiResource{} }
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			x := newSub2APIFixture(t)
			mutate(x)
			x.saveManifest(t)
			_, err := x.p.resolvePlanInspectionScope(context.Background(), x.service, x.action, releaseScopeInput{target: x.b.m.Target})
			if err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestSub2APIFileReplacementAndMaterials(t *testing.T) {
	for _, name := range []string{"bytes", "symlink", "parent symlink", "extra helper", "env bytes", "completed", "unknown json", "manifest bytes"} {
		t.Run(name, func(t *testing.T) {
			x := newSub2APIFixture(t)
			path := filepath.Join(x.b.m.BundleRoot, sub2apiLegacy)
			switch name {
			case "bytes":
				writeSub2APIFile(t, path, []byte("replacement"))
			case "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(x.b.m.Prepared, path); err != nil {
					t.Fatal(err)
				}
			case "parent symlink":
				dir := filepath.Dir(path)
				if err := os.Rename(dir, dir+"-moved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dir+"-moved", dir); err != nil {
					t.Fatal(err)
				}
			case "extra helper":
				writeSub2APIFile(t, filepath.Join(x.b.m.BundleRoot, "extra.py"), []byte("unknown"))
			case "env bytes":
				writeSub2APIFile(t, x.b.m.Runtime.EnvFile, []byte("DOCKER_HOST=override"))
			case "completed":
				raw, _ := os.ReadFile(x.b.m.Prepared)
				raw = []byte(strings.Replace(string(raw), "prepared", "completed", 1))
				writeSub2APIFile(t, x.b.m.Prepared, raw)
				f := x.b.m.Files["evidence/prepared.json"]
				f.Digest = model.WorkDigest(string(raw))
				x.b.m.Files["evidence/prepared.json"] = f
				x.saveManifest(t)
			case "unknown json":
				raw, _ := json.Marshal(x.b.m)
				raw = append([]byte(`{"Undeclared":true,`), raw[1:]...)
				writeSub2APIFile(t, x.p.manifestPath, raw)
				x.p.manifestDigest = model.WorkDigest(string(raw))
				x.b.digest = x.p.manifestDigest
			case "manifest bytes":
				writeSub2APIFile(t, x.p.manifestPath, []byte("{}"))
			}
			_, err := x.p.resolvePlanInspectionScope(context.Background(), x.service, x.action, releaseScopeInput{target: x.b.m.Target})
			if err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestSub2APIStageSequenceAndScope(t *testing.T) {
	x := newSub2APIFixture(t)
	for _, phase := range sub2apiPhases {
		if c := x.run(t, phase); c.Err != nil {
			t.Fatal(c.Err)
		}
		if _, err := x.p.resolvePlanInspectionScope(context.Background(), x.service, x.action, releaseScopeInput{target: x.b.m.Target, call: x.call}); err != nil {
			t.Fatal(phase, err)
		}
	}
	if _, err := x.p.resolvePlanInspectionScope(context.Background(), x.service, x.action, releaseScopeInput{target: x.b.m.Target}); err == nil {
		t.Fatal("creation accepted target")
	}
	if len(x.b.calls) != 7 {
		t.Fatal(x.b.calls)
	}
	for i, r := range x.b.calls {
		if r.Phase != sub2apiPhases[i] || r.PreparationID != x.call.preparationID || r.Target != x.b.m.Target || (i > 0 && r.Previous == "") {
			t.Fatal(r)
		}
	}
	if c := x.p.executeReleaseCall(context.Background(), x.input("rollback")); c.Settled != nil {
		t.Fatal("standalone rollback accepted")
	}
}

func TestSub2APIFailureTransitions(t *testing.T) {
	for _, partial := range []string{"controlled", "pair", "target", "none"} {
		t.Run(partial, func(t *testing.T) {
			x := newSub2APIFixture(t)
			x.b.fail = "apply"
			x.b.partial = partial
			for _, phase := range sub2apiPhases[:3] {
				x.run(t, phase)
			}
			if c := x.run(t, "apply"); c.Err == nil {
				t.Fatal("failed call became success")
			}
			_, normal := x.p.resolvePlanInspectionScope(context.Background(), x.service, x.action, releaseScopeInput{target: x.b.m.Target, call: x.call})
			if (partial == "controlled" || partial == "pair") && normal == nil {
				t.Fatal("partial is general running state")
			}
			in := releaseScopeInput{target: x.b.m.Target, call: x.call, phase: "rollback"}
			if _, err := x.p.resolveReleaseExecutionScope(context.Background(), x.service, x.action, in); err != nil {
				t.Fatal(err)
			}
			c := x.p.executeReleaseCall(context.Background(), x.input("rollback"))
			if partial == "none" {
				if c.Settled != nil {
					t.Fatal("no mutation rollback")
				}
				return
			}
			if c.Err != nil || c.Settled == nil {
				t.Fatal(c.Err)
			}
			want := x.b.m.Before
			if partial == "target" {
				want.Migration = x.b.m.TargetState.Migration
			}
			if x.call.sub2api.last.After != want {
				t.Fatal("rollback changed schema")
			}
		})
	}
}

func TestSub2APIRejectsWrongCallsAndDrift(t *testing.T) {
	cases := map[string]func(*sub2apiFixture, *ExecuteInput){
		"unknown app":          func(x *sub2apiFixture, in *ExecuteInput) { in.Service.Name = "other" },
		"unknown action":       func(x *sub2apiFixture, in *ExecuteInput) { in.Action = "restart" },
		"discover":             func(x *sub2apiFixture, in *ExecuteInput) { in.Action = "check"; in.Phase = "discover" },
		"prepare":              func(x *sub2apiFixture, in *ExecuteInput) { in.Action = "prepare" },
		"wrong target":         func(x *sub2apiFixture, in *ExecuteInput) { in.Target = "latest" },
		"missing context":      func(x *sub2apiFixture, in *ExecuteInput) { in.releaseContext = nil },
		"out of order":         func(x *sub2apiFixture, in *ExecuteInput) { in.Phase = "apply" },
		"wrong directory":      func(x *sub2apiFixture, in *ExecuteInput) { in.OperationDir = filepath.Join(x.root, "other") },
		"source override":      func(x *sub2apiFixture, in *ExecuteInput) { in.SourceDir = x.root },
		"unexpected migration": func(x *sub2apiFixture, in *ExecuteInput) { x.b.state.Migration = model.WorkDigest("unknown") },
		"unexpected image":     func(x *sub2apiFixture, in *ExecuteInput) { x.b.state.Image = "third" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			x := newSub2APIFixture(t)
			in := x.input("preflight")
			change(x, &in)
			c := x.p.executeReleaseCall(context.Background(), in)
			if c.Settled != nil || len(x.b.calls) > 0 {
				t.Fatal("advanced")
			}
		})
	}
}

func TestSub2APIReceiptRejectsIncompleteOrForgedFacts(t *testing.T) {
	cases := map[string]func(*sub2apiCompletion){
		"writer alive":      func(c *sub2apiCompletion) { c.Facts.Successors.Status = "running" },
		"host alive":        func(c *sub2apiCompletion) { c.Facts.Host.Status = "running" },
		"daemon alive":      func(c *sub2apiCompletion) { c.Facts.Daemon.Status = "running" },
		"migration running": func(c *sub2apiCompletion) { c.Facts.Migration.Status = "running" },
		"bgsave timeout":    func(c *sub2apiCompletion) { c.Facts.BGSave.Status = "unknown" },
		"cleanup failed":    func(c *sub2apiCompletion) { c.Facts.Cleanup.Status = "failed" },
		"missing proof":     func(c *sub2apiCompletion) { c.Facts.Host.Proof = "" },
		"wrong call":        func(c *sub2apiCompletion) { c.Request.CallID = "wrong" },
		"wrong plan":        func(c *sub2apiCompletion) { c.Request.PlanID = "wrong" },
		"wrong target":      func(c *sub2apiCompletion) { c.Request.Target = "v2.0.0" },
		"wrong stage":       func(c *sub2apiCompletion) { c.Request.Phase = "identity" },
		"wrong predecessor": func(c *sub2apiCompletion) { c.Request.Previous = model.WorkDigest("other") },
		"unknown outcome":   func(c *sub2apiCompletion) { c.Outcome = "unknown" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			x := newSub2APIFixture(t)
			x.b.completeHook = change
			c := x.p.executeReleaseCall(context.Background(), x.input("preflight"))
			if c.Settled != nil || !x.call.sub2api.blocked {
				t.Fatal("settled")
			}
			x.b.completeHook = nil
			c = x.p.executeReleaseCall(context.Background(), x.input("preflight"))
			if c.Settled != nil || len(x.b.calls) != 1 {
				t.Fatal("late retry advanced")
			}
		})
	}
}

func TestSub2APIReceiptPersistenceCancellationAndReplay(t *testing.T) {
	for _, mode := range []string{"save failed", "tampered readback", "canceled", "duplicate phase", "cross execution"} {
		t.Run(mode, func(t *testing.T) {
			x := newSub2APIFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "save failed":
				x.b.saveHook = func([]byte) ([]byte, error) { return nil, errors.New("disk failed with secret owner token") }
			case "tampered readback":
				x.b.saveHook = func(raw []byte) ([]byte, error) {
					return []byte(strings.Replace(string(raw), "success", "failed", 1)), nil
				}
			case "canceled":
				x.b.completeHook = func(c *sub2apiCompletion) { cancel() }
			case "duplicate phase":
				x.run(t, "preflight")
			case "cross execution":
				x.run(t, "preflight")
				x.call.leaseID = mustUUID(t)
			}
			c := x.p.executeReleaseCall(ctx, x.input("preflight"))
			if c.Settled != nil {
				t.Fatal("settled")
			}
			raw, _ := json.Marshal(c.Result)
			if strings.Contains(string(raw), "secret") || strings.Contains(c.Err.Error(), "secret") {
				t.Fatal("leaked backend error")
			}
		})
	}
}

func TestSub2APIJSONAndAlertScope(t *testing.T) {
	var v sub2apiManifest
	if decodeSub2API([]byte(`{"Version":1,"Version":2}`), &v) == nil {
		t.Fatal("duplicate key accepted")
	}
	for _, mode := range []string{"nginx omitted", "alert selector", "alert labels", "credential endpoint"} {
		t.Run(mode, func(t *testing.T) {
			x := newSub2APIFixture(t)
			switch mode {
			case "nginx omitted":
				x.service.TrafficPolicy = &model.TrafficPolicy{Hostname: "synthetic.invalid"}
			case "alert selector":
				r := x.b.m.Resources["alertmanager"]
				r.Selector = "service=sub2api"
				x.b.m.Resources["alertmanager"] = r
			case "alert labels":
				delete(x.b.m.Alerts.Matchers, "tenantId")
			case "credential endpoint":
				x.b.m.Alerts.Endpoint = "https://user:secret@synthetic.invalid"
			}
			x.saveManifest(t)
			if _, err := x.p.resolvePlanInspectionScope(context.Background(), x.service, x.action, releaseScopeInput{target: x.b.m.Target}); err == nil {
				t.Fatal("inexact scope accepted")
			}
		})
	}
}

func TestSub2APIFailureAtEveryStage(t *testing.T) {
	for i, phase := range sub2apiPhases {
		t.Run(phase, func(t *testing.T) {
			x := newSub2APIFixture(t)
			x.b.fail = phase
			for _, previous := range sub2apiPhases[:i] {
				x.run(t, previous)
			}
			if c := x.run(t, phase); c.Err == nil {
				t.Fatal("lost failure")
			}
			if i > 3 {
				if c := x.run(t, "rollback"); c.Err != nil {
					t.Fatal(c.Err)
				}
			}
		})
	}
}

func TestSub2APIReceiptDuplicateAndCrossChain(t *testing.T) {
	x := newSub2APIFixture(t)
	x.run(t, "preflight")
	r := x.call.sub2api.last
	raw, _ := json.Marshal(r)
	if _, err := x.b.commitReceipt(context.Background(), r.Request.CallID, raw); err == nil {
		t.Fatal("overwrote receipt")
	}
	before := len(x.b.calls)
	x.call = &releaseCallContext{preparationID: mustUUID(t), planID: mustUUID(t), taskID: mustUUID(t), leaseID: mustUUID(t), target: x.b.m.Target, scopeDigest: model.WorkDigest("other"), sub2api: x.call.sub2api}
	if c := x.p.executeReleaseCall(context.Background(), x.input("backup")); c.Settled != nil || len(x.b.calls) != before {
		t.Fatal("borrowed predecessor")
	}
}

func TestSub2APIRevisionAndSourceAreSeparate(t *testing.T) {
	x := newSub2APIFixture(t)
	if len(x.b.m.TargetState.Commit) != 40 || x.b.m.TargetState.Commit == x.b.m.SourceDigest {
		t.Fatal("fixture conflates commit and source")
	}
	if x.b.m.Before.ImageID == "sha256:"+x.b.m.BeforeImageBundleDigest || x.b.m.TargetState.ImageID == "sha256:"+x.b.m.TargetImageBundleDigest {
		t.Fatal("fixture conflates image config and archive")
	}
	x.run(t, "preflight")
	if x.call.sub2api.last.After.Commit != strings.Repeat("a", 40) {
		t.Fatal("lost git revision")
	}
	for _, path := range []string{"evidence/source.bundle", "evidence/before-image.bundle", "evidence/target-image.bundle"} {
		t.Run(path, func(t *testing.T) {
			x := newSub2APIFixture(t)
			b := []byte("replaced material")
			writeSub2APIFile(t, filepath.Join(x.b.m.BundleRoot, path), b)
			f := x.b.m.Files[path]
			f.Digest = model.WorkDigest(string(b))
			x.b.m.Files[path] = f
			x.saveManifest(t)
			if _, err := x.p.resolvePlanInspectionScope(context.Background(), x.service, x.action, releaseScopeInput{target: x.b.m.Target}); err == nil {
				t.Fatal("unbound material accepted")
			}
		})
	}
}

func TestSub2APIStreamsImmutableMaterialHashes(t *testing.T) {
	x := newSub2APIFixture(t)
	b := []byte(strings.Repeat("synthetic-tool", 200000))
	writeSub2APIFile(t, filepath.Join(x.b.m.BundleRoot, "tools/docker"), b)
	f := x.b.m.Files["tools/docker"]
	f.Digest = model.WorkDigest(string(b))
	x.b.m.Files["tools/docker"] = f
	x.saveManifest(t)
	if _, err := x.p.resolvePlanInspectionScope(context.Background(), x.service, x.action, releaseScopeInput{target: x.b.m.Target}); err != nil {
		t.Fatal("large immutable file rejected", err)
	}
}

func TestSub2APIToolsAndWrongFactIdentity(t *testing.T) {
	for _, tool := range []string{"nice", "chmod", "mktemp", "rm", "mv", "cp", "mkdir", "dirname", "basename", "grep", "tail"} {
		t.Run(tool, func(t *testing.T) {
			x := newSub2APIFixture(t)
			delete(x.b.m.Files, "tools/"+tool)
			x.saveManifest(t)
			if _, err := x.p.resolvePlanInspectionScope(context.Background(), x.service, x.action, releaseScopeInput{target: x.b.m.Target}); err == nil {
				t.Fatal("missing tool accepted")
			}
		})
	}
	x := newSub2APIFixture(t)
	x.b.completeHook = func(c *sub2apiCompletion) { c.Facts.BGSave.Identity = model.WorkDigest("other redis instance") }
	if c := x.p.executeReleaseCall(context.Background(), x.input("preflight")); c.Settled != nil {
		t.Fatal("wrong instance accepted")
	}
}
