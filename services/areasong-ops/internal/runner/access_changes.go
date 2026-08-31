package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func (engine *Engine) AccessChanges(ctx context.Context, actor string) ([]model.AccessChange, error) {
	if err := engine.authorizePlatform(ctx, actor, model.PermissionRead, "access"); err != nil {
		return nil, err
	}
	return engine.store.ListAccessChanges(ctx, 100)
}

// CreateAccessChange creates the approval envelope for a high-risk RBAC
// mutation. The actual policy is not changed until a third, independent
// executor calls ApplyAccessChange after two approvals are recorded.
func (engine *Engine) CreateAccessChange(
	ctx context.Context,
	actor string,
	request model.AccessControlUpdateRequest,
) (model.AccessChange, bool, error) {
	if !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.AccessChange{}, false, errors.New("访问策略审批幂等键无效")
	}
	if err := engine.authorizePlatform(ctx, actor, model.PermissionManageAccess, "access"); err != nil {
		return model.AccessChange{}, false, err
	}
	if !request.RequiresDualApproval {
		return model.AccessChange{}, false, errors.New("高风险访问策略变更必须显式要求双人批准")
	}
	if request.Enforced != nil && !*request.Enforced && engine.catalog.SchemaVersion >= 4 {
		return model.AccessChange{}, false, errors.New("生产 schema 4 不允许关闭访问策略")
	}
	// ChangeID is assigned by the store and must not be part of the immutable
	// caller payload. Keeping the original idempotency key inside the payload
	// makes an apply retry resolve to the same durable mutation receipt.
	request.ChangeID = ""
	payload, err := json.Marshal(request)
	if err != nil {
		return model.AccessChange{}, false, err
	}
	digest := digestText(string(payload))
	id, err := newUUID()
	if err != nil {
		return model.AccessChange{}, false, err
	}
	change := model.AccessChange{
		ID: id, IdempotencyKey: request.IdempotencyKey, RequestDigest: digest,
		ActorHash: actor, State: model.AccessChangePendingApproval,
		ConfirmationPhrase:   fmt.Sprintf("批准访问策略变更 %s", shortDigest(digest)),
		RequiresDualApproval: true,
	}
	stored, created, err := engine.store.CreateAccessChange(ctx, change, string(payload), store.HashConfirmation(change.ConfirmationPhrase))
	if err != nil {
		return model.AccessChange{}, false, err
	}
	if created {
		_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{
			ActorHash: actor, Event: "access.change.created", Resource: "access/" + change.ID,
			Outcome: "accepted", Detail: map[string]any{"digest": digest},
		})
	}
	return stored, created, nil
}

func (engine *Engine) ApproveAccessChange(
	ctx context.Context,
	actor, id string,
	request model.AccessChangeApprovalRequest,
) (model.AccessChange, error) {
	if err := engine.authorizePlatform(ctx, actor, model.PermissionManageAccess, "access"); err != nil {
		return model.AccessChange{}, err
	}
	change, err := engine.store.ApproveAccessChange(ctx, id, actor, request.Digest, request.Confirmation)
	if err != nil {
		return model.AccessChange{}, err
	}
	_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{
		ActorHash: actor, Event: "access.change.approved", Resource: "access/" + id,
		Outcome: "accepted", Detail: map[string]any{"state": change.State},
	})
	return change, nil
}

func (engine *Engine) ApplyAccessChange(ctx context.Context, actor, id string) (model.AccessChange, error) {
	if err := engine.authorizePlatform(ctx, actor, model.PermissionManageAccess, "access"); err != nil {
		return model.AccessChange{}, err
	}
	change, payload, err := engine.store.GetAccessChangeWithPayload(ctx, id)
	if err != nil {
		return model.AccessChange{}, err
	}
	if change.State != model.AccessChangeApproved {
		if change.State == model.AccessChangeApplied {
			return change, nil
		}
		return model.AccessChange{}, errors.New("访问策略变更尚未完成双人批准")
	}
	if actor == change.ActorHash || actor == change.ApprovedByHash || actor == change.SecondApprovedByHash {
		return model.AccessChange{}, errors.New("访问策略变更执行人必须独立于创建人与批准人")
	}
	var request model.AccessControlUpdateRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return model.AccessChange{}, errors.New("访问策略审批载荷损坏")
	}
	request.RequiresDualApproval = false
	request.ChangeID = id
	if _, err := engine.UpdateAccess(ctx, actor, request); err != nil {
		return change, err
	}
	applied, err := engine.store.MarkAccessChangeApplied(ctx, id, actor)
	if err != nil {
		return change, err
	}
	_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{
		ActorHash: actor, Event: "access.change.applied", Resource: "access/" + id,
		Outcome: "accepted", Detail: map[string]any{"changeId": id},
	})
	return applied, nil
}

func (engine *Engine) RejectAccessChange(ctx context.Context, actor, id, reason string) (model.AccessChange, error) {
	if err := engine.authorizePlatform(ctx, actor, model.PermissionManageAccess, "access"); err != nil {
		return model.AccessChange{}, err
	}
	change, err := engine.store.RejectAccessChange(ctx, id, actor, reason)
	if err != nil {
		return model.AccessChange{}, err
	}
	_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{
		ActorHash: actor, Event: "access.change.rejected", Resource: "access/" + id,
		Outcome: "accepted", Detail: map[string]any{"reason": reason},
	})
	return change, nil
}
