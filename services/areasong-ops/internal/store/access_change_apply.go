package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type accessChangeExecution struct {
	ChangeID, Actor, RequestDigest, IdempotencyKey string
}

// 共用审批核验与成功重放；事务只由最外层提交。
func accessChangeForApplyTx(ctx context.Context, tx *sql.Tx, execution accessChangeExecution) (model.AccessChange, string, error) {
	change, payload, err := scanAccessChange(tx.QueryRowContext(ctx, accessChangeSelectWithPayload+` WHERE id=?`, execution.ChangeID), true)
	if errors.Is(err, sql.ErrNoRows) {
		return model.AccessChange{}, "", ErrNotFound
	}
	if err != nil {
		return model.AccessChange{}, "", err
	}
	if change.IdempotencyKey != execution.IdempotencyKey {
		return model.AccessChange{}, "", ErrIdempotency
	}
	if execution.RequestDigest == "" || change.RequestDigest != execution.RequestDigest {
		return model.AccessChange{}, "", ErrIdempotency
	}
	if change.State == model.AccessChangeApplied {
		if change.AppliedByHash != execution.Actor {
			return model.AccessChange{}, "", ErrActorMismatch
		}
		// The durable change envelope is the idempotency authority once the
		// change is applied. Do not rebuild or compare the policy digest here:
		// an unrelated policy update between two retries must not turn a
		// successful execution into a false conflict.
		return change, payload, nil
	}
	if change.State != model.AccessChangeApproved {
		return model.AccessChange{}, "", errors.New("访问策略变更尚未完成双人批准")
	}
	if model.UsesTwoPartyApproval(change.ApprovalPolicy) {
		if execution.Actor != change.ActorHash || change.ApprovedByHash == "" || change.ApprovedByHash == execution.Actor {
			return model.AccessChange{}, "", fmt.Errorf("%w: 访问策略变更需要由创建人执行，且批准人必须独立", ErrActorMismatch)
		}
	} else if execution.Actor == change.ActorHash || execution.Actor == change.ApprovedByHash || execution.Actor == change.SecondApprovedByHash {
		return model.AccessChange{}, "", errors.New("访问策略变更执行人必须独立于创建人与批准人")
	}
	// 上方 AccessChangeDigest 绑定 Runner 已验证的摘要；这里验证事务内原始载荷，
	// 两者共同拒绝读取后载荷单独变化或载荷与摘要一起变化。成功重放不重新验载荷。
	if digestPolicyJSON(payload) != change.RequestDigest {
		return model.AccessChange{}, "", ErrIdempotency
	}
	return change, payload, nil
}

func (store *Store) finishAccessChangeTx(ctx context.Context, tx *sql.Tx, change model.AccessChange, actor string, snapshot model.AccessPolicySnapshot) (model.AccessChange, error) {
	now := store.now()
	result, err := tx.ExecContext(ctx, `UPDATE access_changes
		SET state=?,applied_by_hash=?,applied_policy_digest=?,applied_policy_version=?,applied_at=?,error=''
		WHERE id=? AND state=?`, model.AccessChangeApplied, actor, snapshot.Digest, snapshot.Version,
		timeText(now), change.ID, model.AccessChangeApproved)
	if err := requireOne(result, err, "访问策略变更收口失败"); err != nil {
		return model.AccessChange{}, err
	}
	if err := appendPlanAudit(ctx, tx, model.AuditEntry{
		ActorHash: actor, Event: "access.change.applied", Resource: "access/" + change.ID,
		Outcome: "accepted", Detail: map[string]any{
			"changeId": change.ID, "policyDigest": snapshot.Digest, "policyVersion": snapshot.Version,
		},
	}, now); err != nil {
		return model.AccessChange{}, err
	}
	change.State = model.AccessChangeApplied
	change.AppliedByHash = actor
	change.AppliedPolicyDigest = snapshot.Digest
	change.AppliedPolicyVersion = snapshot.Version
	change.AppliedAt = &now
	return change, nil
}

// 保留 Runner hasPlatformAdmin/principalUsable 的既有筛选，不扩展其绑定分支语义。
func ensureLifecyclePlatformAdmin(policy config.AccessPolicy, now time.Time) error {
	filtered := policy
	filtered.Principals = make(map[string]config.AccessPrincipal)
	for subject, principal := range policy.Principals {
		if lifecyclePrincipalUsable(policy, subject, principal, now) {
			filtered.Principals[subject] = principal
		}
	}
	raw, err := json.Marshal(filtered)
	if err != nil {
		return err
	}
	return ensurePolicyHasPlatformAdmin(string(raw))
}

func lifecyclePrincipalUsable(policy config.AccessPolicy, subject string, principal config.AccessPrincipal, now time.Time) bool {
	if (principal.Status != "" && principal.Status != "active") || (principal.ExpiresAt != nil && !now.Before(*principal.ExpiresAt)) {
		return false
	}
	if !principal.JIT {
		return true
	}
	for _, binding := range policy.Bindings {
		if !binding.JIT || binding.Subject != subject {
			continue
		}
		if binding.TenantID != "*" && binding.TenantID != principal.TenantID {
			continue
		}
		if binding.ExpiresAt != nil && !now.Before(*binding.ExpiresAt) {
			continue
		}
		return true
	}
	return false
}
