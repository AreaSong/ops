package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestPreparationRevocationWhileInspectionRuns(t *testing.T) {
	engine, profile := preparationEngine(t)
	profile.entered, profile.release = make(chan struct{}), make(chan struct{})
	var release sync.Once
	unblock := func() { release.Do(func() { close(profile.release) }) }
	t.Cleanup(unblock)
	request := bpRequest(t)
	done := make(chan error, 1)
	go func() {
		_, err := engine.createManualReleasePlan(context.Background(), actorHash(), request)
		done <- err
	}()
	select {
	case <-profile.entered:
	case err := <-done:
		t.Fatalf("未到达检查: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("检查屏障超时")
	}
	second, err := store.Open(filepath.Join(engine.stateRoot, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	policy, snapshot, err := engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	principal := policy.Principals[actorHash()]
	principal.Status = "disabled"
	policy.Principals[actorHash()] = principal
	raw, _ := json.Marshal(policy)
	_, err = second.SaveAccessPolicySnapshot(context.Background(), model.AccessPolicySnapshot{ActorHash: stringsForHistoricalApprover(),
		PolicyJSON: string(raw), Digest: "sha256:" + model.WorkDigest(string(raw))}, snapshot.Version)
	if err != nil {
		t.Fatal(err)
	}
	unblock()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("检查中撤权仍生成计划")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("撤权后检查未结束")
	}
	record, found, err := second.GetPlanPreparationByRequest(context.Background(), request.IdempotencyKey)
	if err != nil || !found || record.State != model.PlanInspectionDone || record.ProducedPlanID != "" {
		t.Fatal("撤权后登记未保守留存", err)
	}
	if profile.calls != 1 {
		t.Fatal("撤权导致重复检查")
	}
}

type lateInspection struct {
	*syntheticInspection
	started chan string
	release chan struct{}
	stopped chan struct{}
	result  chan error
}

func (profile *lateInspection) executePlanInspection(_ context.Context, input ExecuteInput) inspectionCall {
	go func() {
		profile.started <- input.OperationDir
		<-profile.release
		profile.result <- os.WriteFile(filepath.Join(input.OperationDir, "late-result"), []byte("late synthetic response"), 0600)
		close(profile.stopped)
	}()
	return inspectionCall{Result: model.AdapterResult{OK: true, Summary: "迟到合成检查", Data: map[string]any{"currentVersion": "1.0.0"}},
		Settled: profile.stopped, EvidenceDigest: model.WorkDigest("late synthetic inspection")}
}

func TestPreparationLateCompletionCannotProcessOrCloseUncertain(t *testing.T) {
	engine, base := preparationEngine(t)
	profile := &lateInspection{syntheticInspection: base, started: make(chan string, 1), release: make(chan struct{}), stopped: make(chan struct{}), result: make(chan error, 1)}
	engine.executor = profile
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var once sync.Once
	finish := func() { once.Do(func() { close(profile.release) }) }
	t.Cleanup(finish)
	request := bpRequest(t)
	done := make(chan error, 1)
	go func() { _, err := engine.createManualReleasePlan(ctx, actorHash(), request); done <- err }()
	var directory string
	select {
	case directory = <-profile.started:
	case err := <-done:
		t.Fatalf("未到达异步检查: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("异步检查超时")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("未收敛调用被关闭")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后协调者未退出")
	}
	before, _, err := engine.store.GetPlanPreparationByRequest(context.Background(), request.IdempotencyKey)
	if err != nil || before.State != model.WorkUncertain {
		t.Fatal("未保持不确定登记", err)
	}
	if _, err = os.Stat(directory); err != nil {
		t.Fatal("在途检查目录被提前清理", err)
	}
	finish()
	select {
	case <-profile.stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("迟到回调未结束")
	}
	if err = <-profile.result; err != nil {
		t.Fatal("在途工作目录被破坏", err)
	}
	after, _, err := engine.store.GetPlanPreparationByRequest(context.Background(), request.IdempotencyKey)
	if err != nil || after.State != before.State || after.Revision != before.Revision || after.ResultJSON != "" || after.ProducedPlanID != "" {
		t.Fatal("迟到结果被再次处理或关闭", err)
	}
	if _, err = os.Stat(filepath.Join(directory, "late-result")); err != nil {
		t.Fatal("没有形成真实迟到结果", err)
	}
}
