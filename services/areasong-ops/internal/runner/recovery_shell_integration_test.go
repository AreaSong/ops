package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

type selectedRecoveryExecutor struct {
	script      string
	environment []string
	before      map[string]any
}

func (executor *selectedRecoveryExecutor) Execute(ctx context.Context, input ExecuteInput) (model.AdapterResult, error) {
	if input.Action == "inspect" {
		return model.AdapterResult{OK: true, Summary: "isolated identity", Data: cloneAnyMap(executor.before)}, nil
	}
	if input.Action != "restore-drill" {
		return model.AdapterResult{}, errors.New("unexpected non-isolated action")
	}
	command := exec.CommandContext(ctx, "bash", executor.script, input.Action, input.Phase, input.OperationDir, input.Target, input.SourceDir)
	command.Env = append(os.Environ(), executor.environment...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return model.AdapterResult{}, errors.New(stderr.String())
	}
	var result model.AdapterResult
	if err := json.Unmarshal(output, &result); err != nil {
		return result, err
	}
	if !result.OK {
		return result, errors.New("isolated adapter refused")
	}
	return result, nil
}

func TestRestorePlanRunsRealShellWithSelectedPointNotLatest(t *testing.T) {
	_, filename, _, _ := runtime.Caller(0)
	scripts := filepath.Clean(filepath.Join(filepath.Dir(filename), "../../../../scripts/backup"))
	engine, database := testEngine(t, &fakeExecutor{})
	root := engine.stateRoot
	backups := filepath.Join(root, "backups")
	fixtureCommand := exec.Command("python3", filepath.Join(scripts, "tests/recovery_fixture.py"), "--root", filepath.Join(backups, "A"))
	output, err := fixtureCommand.Output()
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Artifacts []model.RecoveryArtifact
		Before    map[string]any
	}
	if err := json.Unmarshal(output, &fixture); err != nil {
		t.Fatal(err)
	}
	fixture.Before["diagnostic"] = map[string]any{"url": "https://example.invalid/?a=1&b=<test>",
		"ratio": 0.000001, "tiny": 1e-7, "negativeZero": math.Copysign(0, -1)}
	if output, err := exec.Command("python3", filepath.Join(scripts, "tests/recovery_fixture.py"), "--root", filepath.Join(backups, "B"), "--label", "B").CombinedOutput(); err != nil {
		t.Fatalf("second fixture: %s %v", output, err)
	}
	if err := os.WriteFile(filepath.Join(backups, "latest-manifest.txt"), []byte("B"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"id":     "#!/bin/sh\nif [ \"$1\" = -u ]; then echo 0; else exec /usr/bin/id \"$@\"; fi\n",
		"chown":  "#!/bin/sh\nexit 0\n",
		"curl":   "#!/bin/sh\necho 'unexpected host network request' >&2\nexit 91\n",
		"docker": selectedRecoveryDocker,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	imported := filepath.Join(root, "imported.sql")
	executor := &selectedRecoveryExecutor{script: filepath.Join(scripts, "restore-sub2api-isolated.sh"), before: fixture.Before,
		environment: []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "BACKUP_ROOT=" + backups,
			"TEST_RESTORE_ROOT=" + root, "TEST_RESTORE_IMPORT=" + imported,
			"SUB2API_RESTORE_ENV_FILE=/must-not-read-live-env", "SUB2API_RESTORE_BACKUP_POSTGRES=/must-not-run",
			"SUB2API_RESTORE_BACKUP_REDIS=/must-not-run", "SUB2API_RESTORE_BACKUP_VOLUMES=/must-not-run"}}
	engine.executor = executor
	engine.backupRoot = backups
	service := enableRecoveryActions(engine)
	delete(engine.catalog.Services, "demo")
	service.Name, service.ObjectID, service.ServerID = "sub2api", "service:sub2api", "local"
	service.RecoveryPointPolicy.RequiredArtifactRoles = []string{"postgres-sub2api", "redis", "volume-sub2api-data", "configs", "runtime-snapshot"}
	action := service.Actions["restore-drill"]
	action.Steps = []string{"preflight", "backup", "drill", "verify"}
	service.Actions["restore-drill"] = action
	engine.catalog.Services["sub2api"] = service
	point := selectedRecoveryPoint(t, engine, database, service, fixture.Before, fixture.Artifacts)
	ctx := context.Background()
	plan, err := engine.CreateRestorePlan(ctx, actorHash(), model.RestoreRequest{Service: "sub2api", RecoveryPointID: point.ID, Mode: "isolated",
		Confirmation: "创建隔离恢复演练计划 sub2api", IdempotencyKey: mustUUID(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ApproveReleasePlan(ctx, actorHash(), plan.ID, model.ApprovePlanRequest{Digest: plan.Digest, Confirmation: plan.ConfirmationPhrase}); err != nil {
		t.Fatal(err)
	}
	task, _, err := engine.ExecuteReleasePlan(ctx, actorHash(), plan.ID, model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)})
	if err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	finished, err := database.GetTask(ctx, task.ID)
	if err != nil || finished.State != model.TaskSucceeded {
		t.Fatalf("real shell restore=%+v err=%v", finished, err)
	}
	data, err := os.ReadFile(imported)
	if err != nil || !bytes.Contains(data, []byte("SELECT 'A'")) || bytes.Contains(data, []byte("SELECT 'B'")) {
		t.Fatalf("wrong imported backup: %q %v", data, err)
	}
	for _, artifact := range fixture.Artifacts {
		digest, err := fileSHA256(artifact.Path)
		if err != nil || "sha256:"+digest != artifact.SHA256 {
			t.Fatalf("source artifact changed: %s %v", artifact.Role, err)
		}
	}
}

func selectedRecoveryPoint(t *testing.T, engine *Engine, database *store.Store, service model.ServiceDefinition, before map[string]any, artifacts []model.RecoveryArtifact) model.RecoveryPoint {
	t.Helper()
	ctx, now := context.Background(), time.Now().UTC()
	preview := model.Preview{ID: mustUUID(t), ActorHash: actorHash(), Service: service.Name, Action: "backup", Risk: model.RiskMedium,
		Steps: []string{"backup"}, Snapshot: before, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := database.CreatePreview(ctx, store.PreviewInput{Preview: preview, ConfirmationHash: store.HashConfirmation("backup")}); err != nil {
		t.Fatal(err)
	}
	task, _, err := database.StartTask(ctx, actorHash(), model.StartTaskRequest{PreviewID: preview.ID, Confirmation: "backup", IdempotencyKey: mustUUID(t)}, mustUUID(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunningOwned(ctx, task.ID, "backup", engine.owner); err != nil {
		t.Fatal(err)
	}
	point, err := engine.persistRecoveryPoint(ctx, task, service, &model.RecoveryPointEvidence{SchemaVersion: 1, Service: service.Name,
		TaskID: task.ID, CreatedAt: now, Artifacts: artifacts})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.FinishTask(ctx, task.ID, model.TaskSucceeded, "fixture backup", ""); err != nil {
		t.Fatal(err)
	}
	return point
}

const selectedRecoveryDocker = `#!/usr/bin/env python3
import json,os,sys
from pathlib import Path
args=sys.argv[1:]
root=Path(os.environ["TEST_RESTORE_ROOT"]).resolve()
if args[0]=="compose":
    assert args[args.index("--project-name")+1].startswith("ops-sub2api-")
    compose=Path(args[args.index("-f")+1]).resolve()
    assert compose.is_relative_to(root)
    text=compose.read_text()
    assert "internal: true" in text and "\n    ports:" not in text
    assert Path(args[args.index("--env-file")+1]).resolve().is_relative_to(root)
    if "ps" in args:
        print("isolated-"+args[-1])
    sys.exit(0)
if args[0]=="logs":
    assert args[1]=="isolated-postgres"
    print("PostgreSQL init process complete; ready for start up.")
    sys.exit(0)
if args[0]=="exec":
    container=args[2] if args[1]=="-i" else args[1]
    assert container.startswith("isolated-")
    if "psql" in args:
        if "-i" in args:
            Path(os.environ["TEST_RESTORE_IMPORT"]).write_bytes(sys.stdin.buffer.read())
        else:
            print("1")
    elif "redis-cli" in args:
        print("PONG")
    elif "wget" not in args:
        raise AssertionError(args)
    sys.exit(0)
raise AssertionError("forbidden Docker call: "+repr(args))
`
