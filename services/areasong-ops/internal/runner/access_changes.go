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
// mutation. New plans use a two-party workflow: an independent approver
// authorizes the creator to apply the mutation.
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
		RequiresDualApproval: true, ApprovalPolicy: model.ApprovalPolicyTwoParty,
	}
	stored, created, err := engine.store.CreateAccessChange(ctx, change, string(payload), store.HashConfirmation(change.ConfirmationPhrase))
	if err != nil {
		return model.AccessChange{}, false, err
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
	return change, nil
}

func (engine *Engine) ApplyAccessChange(ctx context.Context, actor, id string) (model.AccessChange, error) {
	if !actorPattern.MatchString(actor) {
		return model.AccessChange{}, errors.New("操作者标识无效")
	}
	change, payload, err := engine.store.GetAccessChangeWithPayload(ctx, id)
	if err != nil {
		return model.AccessChange{}, err
	}
	if change.State == model.AccessChangeApplied {
		if change.AppliedByHash == actor {
			return change, nil
		}
		return model.AccessChange{}, store.ErrActorMismatch
	}
	if err := engine.authorizePlatform(ctx, actor, model.PermissionManageAccess, "access"); err != nil {
		// A concurrent retry may have read the approved row immediately before
		// the first execution committed a policy that revoked this actor. Re-read
		// the durable envelope before returning the authorization failure.
		latest, readErr := engine.store.GetAccessChange(ctx, id)
		if readErr == nil && latest.State == model.AccessChangeApplied && latest.AppliedByHash == actor {
			return latest, nil
		}
		return model.AccessChange{}, err
	}
	if change.State != model.AccessChangeApproved {
		return model.AccessChange{}, errors.New("访问策略变更尚未完成双人批准")
	}
	if model.UsesTwoPartyApproval(change.ApprovalPolicy) {
		if actor != change.ActorHash || change.ApprovedByHash == "" || actor == change.ApprovedByHash {
			return model.AccessChange{}, errors.New("访问策略变更需要由创建人执行，且批准人必须独立")
		}
	} else if actor == change.ActorHash || actor == change.ApprovedByHash || actor == change.SecondApprovedByHash {
		return model.AccessChange{}, errors.New("访问策略变更执行人必须独立于创建人与批准人")
	}
	var request model.AccessControlUpdateRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return model.AccessChange{}, errors.New("访问策略审批载荷损坏")
	}
	request.RequiresDualApproval = false
	execution := &approvedAccessExecution{changeID: id, requestDigest: change.RequestDigest}
	if _, err := engine.updateAccess(ctx, actor, request, execution); err != nil {
		latest, readErr := engine.store.GetAccessChange(ctx, id)
		if readErr == nil && latest.State == model.AccessChangeApplied && latest.AppliedByHash == actor {
			return latest, nil
		}
		return change, err
	}
	return engine.store.GetAccessChange(ctx, id)
}

func (engine *Engine) RejectAccessChange(ctx context.Context, actor, id, reason string) (model.AccessChange, error) {
	if err := engine.authorizePlatform(ctx, actor, model.PermissionManageAccess, "access"); err != nil {
		return model.AccessChange{}, err
	}
	change, err := engine.store.RejectAccessChange(ctx, id, actor, reason)
	if err != nil {
		return model.AccessChange{}, err
	}
	return change, nil
}
