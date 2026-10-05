package runner

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// 这些事实只能来自私有受控后端，不接受 HTTP、任意文件或 AdapterResult 自报。
// Identity 绑定被观察对象/启动代次；Proof 绑定该后端保留的证据。摘要格式本身不授信。
type sub2apiFact struct{ Status, Identity, Proof string }
type sub2apiFacts struct{ Host, Daemon, Migration, BGSave, Successors, Cleanup sub2apiFact }

type sub2apiCallRequest struct {
	CallID, PreparationID, PlanID, TaskID, LeaseID           string
	ScopeDigest, ImplementationDigest, InputDigest, Previous string
	Target, Action, Phase, Kind, OperationDir                string
}

type sub2apiCompletion struct {
	BackupReceiptDigest  string
	BackupResourceDigest string
	BackupSource         sub2apiRuntimeBaseline
	Request              sub2apiCallRequest
	Outcome              string
	After                sub2apiState
	Facts                sub2apiFacts
	RecoveryPoint        *model.RecoveryPointEvidence
}

type sub2apiReceipt struct {
	Version               int
	Request               sub2apiCallRequest
	Outcome, ResultDigest string
	After                 sub2apiState
	Facts                 sub2apiFacts
}

type sub2apiProgress struct {
	profile                     *sub2apiInspectionProfile
	binding                     string
	last                        *sub2apiReceipt
	next                        int
	blocked, failed, rolledBack bool
}

// Identity 是本次调用与被观察资源的关联摘要；实际进程/exec/迁移批次映射由可信后端
// 留存在 Proof 所指证据中。不能接受其他实例的完成事实或把普通业务进程纳入退出集合。
func sub2apiFactIdentities(m sub2apiManifest, r sub2apiCallRequest) map[string]string {
	resources := map[string]string{"host": r.LeaseID + ":" + r.PreparationID, "daemon": m.Resources["daemon"].Selector,
		"migration": m.Resources["database"].Selector, "bgsave": m.Resources["redis-instance"].Selector,
		"successors": r.ScopeDigest, "cleanup": r.OperationDir + ":" + m.Resources["temporary-root"].Selector}
	for role, resource := range resources {
		resources[role] = sub2apiDigest([]string{r.CallID, role, resource})
	}
	return resources
}

func sub2apiBinding(c *releaseCallContext) string {
	return sub2apiDigest([]string{c.preparationID, c.planID, c.taskID, c.leaseID, c.target, c.scopeDigest})
}

func (p *sub2apiInspectionProfile) progress(c *releaseCallContext) (*sub2apiProgress, error) {
	if c == nil || !uuidPattern.MatchString(c.preparationID) || !uuidPattern.MatchString(c.planID) ||
		!model.ValidWorkDigest(c.scopeDigest) || !sub2apiPlain(c.target) ||
		((c.taskID == "") != (c.leaseID == "")) {
		return nil, errSub2API
	}
	if c.taskID != "" && (!uuidPattern.MatchString(c.taskID) || !uuidPattern.MatchString(c.leaseID)) {
		return nil, errSub2API
	}
	if c.sub2api == nil {
		c.sub2api = &sub2apiProgress{profile: p, binding: sub2apiBinding(c)}
	}
	s := c.sub2api
	if s.profile != p || s.binding != sub2apiBinding(c) || s.blocked {
		return nil, errSub2API
	}
	return s, nil
}

func settledSub2APIFact(f sub2apiFact, required bool, success bool) bool {
	if !model.ValidWorkDigest(f.Proof) || !model.ValidWorkDigest(f.Identity) {
		return false
	}
	if required && success {
		return f.Status == "complete"
	}
	if required {
		return f.Status == "complete" || f.Status == "failed"
	}
	return f.Status == "not-started" || f.Status == "complete" || (!success && f.Status == "failed")
}

func validSub2APIFacts(m sub2apiManifest, c sub2apiCompletion) bool {
	identities := sub2apiFactIdentities(m, c.Request)
	for role, f := range map[string]sub2apiFact{"host": c.Facts.Host, "daemon": c.Facts.Daemon, "migration": c.Facts.Migration, "bgsave": c.Facts.BGSave, "successors": c.Facts.Successors, "cleanup": c.Facts.Cleanup} {
		if f.Identity != identities[role] {
			return false
		}
	}
	success := c.Outcome == "success"
	f := c.Facts
	return settledSub2APIFact(f.Host, true, true) && settledSub2APIFact(f.Daemon, true, success) &&
		settledSub2APIFact(f.Migration, c.Request.Phase == "apply" || c.Request.Phase == "rollback", success) &&
		settledSub2APIFact(f.BGSave, c.Request.Phase == "backup", success) &&
		settledSub2APIFact(f.Successors, true, true) && settledSub2APIFact(f.Cleanup, true, true)
}

func (s *sub2apiProgress) previous() string {
	if s.last == nil {
		return ""
	}
	return sub2apiDigest(s.last)
}

func (s *sub2apiProgress) acceptsPhase(in ExecuteInput) bool {
	if in.Action == "inspect" {
		return !s.failed && !s.rolledBack && (s.next == 0 || s.next == len(sub2apiPhases))
	}
	if in.Phase == "rollback" {
		return s.failed && !s.rolledBack && s.last != nil && s.last.Request.Phase != "rollback"
	}
	return !s.failed && !s.rolledBack && s.next < len(sub2apiPhases) && sub2apiPhases[s.next] == in.Phase
}

func (m sub2apiManifest) partial(s sub2apiState) bool {
	x := m.Before
	x.Controlled = m.TargetState.Controlled
	if s == x {
		return true
	}
	x.Runtime = m.TargetState.Runtime
	return s == x
}

func (m sub2apiManifest) afterAllowed(s *sub2apiProgress, r sub2apiCallRequest, c sub2apiCompletion) bool {
	before := m.Before
	if s.last != nil {
		before = s.last.After
	}
	if r.Action == "inspect" {
		return c.After == before
	}
	if r.Phase == "rollback" {
		if !s.failed || s.last == nil || (before != m.TargetState && !m.partial(before)) {
			return false
		}
		x := m.Before
		x.Migration = before.Migration // 回滚应用产物，不回滚数据。
		return c.Outcome == "success" && c.After == x
	}
	if r.Phase == "apply" {
		if c.Outcome == "success" {
			return c.After == m.TargetState
		}
		return c.After == m.Before || c.After == m.TargetState || m.partial(c.After)
	}
	return c.After == before
}

// commitReceipt 必须原子保存、不覆盖已有 callId、fsync 并重新读取；失败不补造 Settled。
// 只有合成后端在 B1 实现此契约；生产没有默认 journal/执行后端。
func (p *sub2apiInspectionProfile) finishCall(ctx context.Context, m sub2apiManifest, s *sub2apiProgress, req sub2apiCallRequest, c sub2apiCompletion) inspectionCall {
	if ctx.Err() != nil || !reflect.DeepEqual(req, c.Request) || (c.Outcome != "success" && c.Outcome != "failed") ||
		!validSub2APIFacts(m, c) || !m.afterAllowed(s, req, c) {
		return inspectionCall{Err: errSub2API}
	}
	if req.Phase == "backup" && c.Outcome == "success" && validateSub2APIBackup(m, req, c) != nil {
		return inspectionCall{Err: errSub2API}
	}
	result := sub2apiResult(req, c)
	receipt := sub2apiReceipt{Version: 1, Request: req, Outcome: c.Outcome, ResultDigest: sub2apiDigest(result), After: c.After, Facts: c.Facts}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return inspectionCall{Err: errSub2API}
	}
	saved, err := p.backend.commitReceipt(ctx, req.CallID, raw)
	var verified sub2apiReceipt
	if err != nil || ctx.Err() != nil || decodeSub2API(saved, &verified) != nil || !reflect.DeepEqual(receipt, verified) || string(raw) != string(saved) {
		return inspectionCall{Err: errSub2API}
	}
	s.last = &receipt
	s.failed = c.Outcome == "failed"
	if req.Action == "update" && !s.failed {
		if req.Phase == "rollback" {
			s.rolledBack = true
		} else {
			s.next++
		}
	}
	s.blocked = false
	done := make(chan struct{})
	close(done) // 仅在所有后端事实及原子保存/复读验证之后。
	call := inspectionCall{Result: result, Settled: done, EvidenceDigest: sub2apiDigest(receipt)}
	if s.failed {
		call.Err = errSub2API
	}
	return call
}

func sub2apiResult(r sub2apiCallRequest, c sub2apiCompletion) model.AdapterResult {
	// 不传播后端错误正文、响应、env 或任意调试 map；输出固定非敏感投影。
	s := c.After
	return model.AdapterResult{SchemaVersion: 2, Action: r.Action, Phase: r.Phase, OK: c.Outcome == "success",
		Summary: "Sub2API 私有阶段回执已核验", Data: map[string]any{"currentVersion": s.Version, "currentImage": s.Image,
			"currentImageId": s.ImageID, "gitCommit": s.Commit, "runtimeIdentityHash": sub2apiDigest(s)}, RecoveryPoint: c.RecoveryPoint}
}
