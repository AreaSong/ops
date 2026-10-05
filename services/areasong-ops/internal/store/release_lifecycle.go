package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func validateReleaseAuthorityTx(ctx context.Context, tx *sql.Tx, authority model.ReleaseAuthority) error {
	if !model.ValidWorkDigest(authority.ActorHash) {
		return ErrActorMismatch
	}
	policy, snapshot, err := lifecyclePolicyTx(ctx, tx)
	if err != nil {
		return err
	}
	principal, ok := policy.Principals[authority.ActorHash]
	tenant := principal.TenantID
	if tenant == "" {
		tenant = policy.DefaultTenant
	}
	if !policy.Enforced || !ok || authority.Permission != model.PermissionDeploy ||
		(principal.Status != "" && principal.Status != "active") ||
		(principal.ExpiresAt != nil && !time.Now().Before(*principal.ExpiresAt)) || tenant != authority.TenantID ||
		(snapshot.Version != authority.PolicyVersion || snapshot.Digest != authority.PolicyDigest) {
		return ErrTenantLifecycleConflict
	}
	rows, err := tenantRowsTx(ctx, tx)
	if err != nil {
		return err
	}
	row, err := lifecycleMirror(policy, rows, authority.TenantID)
	if err != nil {
		return err
	}
	if lifecycleStatus(row.Status) != "active" || row.Generation <= 0 {
		return ErrTenantLifecycleConflict
	}
	return nil
}

// 权限快照及所有目标在同一个读取事务中捕获；随后的登记仍须在写锁内复验。
func (store *Store) CaptureReleaseLifecycle(ctx context.Context, authority model.ReleaseAuthority, binding model.ReleaseLifecycleBinding) (model.ReleaseLifecycleBinding, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return binding, err
	}
	defer tx.Rollback()
	if err = validateReleaseAuthorityTx(ctx, tx, authority); err != nil {
		return binding, err
	}
	if err = validateReleaseObjectPermissionsTx(ctx, tx, authority, binding.TargetObjects); err != nil {
		return binding, err
	}
	policy, _, err := lifecyclePolicyTx(ctx, tx)
	if err != nil {
		return binding, err
	}
	rows, err := tenantRowsTx(ctx, tx)
	if err != nil {
		return binding, err
	}
	ids := map[string]bool{binding.CreatorTenantID: true}
	for _, object := range binding.TargetObjects {
		ids[object.TenantID] = true
	}
	binding.Targets = nil
	for id := range ids {
		row, e := lifecycleMirror(policy, rows, id)
		if e != nil {
			return binding, e
		}
		if lifecycleStatus(row.Status) != "active" || row.Generation <= 0 {
			return binding, ErrTenantLifecycleConflict
		}
		binding.Targets = append(binding.Targets, model.ReleaseGeneration{TenantID: id, ExpectedGeneration: strconv.FormatInt(row.Generation, 10)})
	}
	sort.Slice(binding.Targets, func(i, j int) bool { return binding.Targets[i].TenantID < binding.Targets[j].TenantID })
	if _, err = binding.WorkTargets(); err != nil {
		return binding, err
	}
	return binding, tx.Commit()
}

func validatePreparationTargetsTx(ctx context.Context, tx *sql.Tx, r model.PlanPreparationRequest) error {
	if err := validateReleaseAuthorityTx(ctx, tx, r.Authority); err != nil {
		return err
	}
	targets, err := r.Binding.WorkTargets()
	if err != nil {
		return err
	}
	if err := validateReleaseObjectPermissionsTx(ctx, tx, r.Authority, r.Binding.TargetObjects); err != nil {
		return err
	}
	return validateWorkTargetsTx(ctx, tx, targets)
}

func validatePreparedPlanTx(ctx context.Context, tx *sql.Tx, plan model.ReleasePlan) error {
	if plan.ApprovalSummary.SchemaVersion != 2 || plan.ApprovalSummary.Lifecycle == nil {
		return model.ErrReleaseLifecycle
	}
	digest, err := model.ReleaseApprovalDigest(plan.ApprovalSummary)
	if err != nil || digest != plan.Digest {
		return model.ErrReleaseLifecycle
	}
	record, _, err := readPlanPreparationTx(ctx, tx, plan.ApprovalSummary.Lifecycle.PreparationID)
	if err != nil {
		return err
	}
	if record.State != model.WorkClosed || record.ProducedPlanID != plan.ID || record.CloseKind != "inspection_settled" {
		return model.ErrReleaseLifecycle
	}
	if err = matchPreparedPlan(record, plan); err != nil {
		return err
	}
	targets, err := record.Request.Binding.WorkTargets()
	if err != nil {
		return err
	}
	return validateWorkTargetsTx(ctx, tx, targets)
}

func matchPreparedPlan(record model.PlanPreparation, plan model.ReleasePlan) error {
	r := record.Request
	if plan.ID != r.PlanID || plan.ActorHash != r.Authority.ActorHash || plan.RequestIdempotencyKey != r.IdempotencyKey ||
		plan.RequestDigest != r.InputDigest || plan.Service != r.Service || plan.Action != r.Action || plan.Target != r.Target {
		return model.ErrReleaseLifecycle
	}
	s := plan.ApprovalSummary
	if s.Service != plan.Service || s.Action != plan.Action || s.Target != plan.Target || s.TenantID != plan.TenantID ||
		s.ServerID != plan.ServerID || s.Risk != plan.Risk || s.ApprovalPolicy != plan.ApprovalPolicy || s.ObservationSeconds != plan.ObservationSeconds ||
		!sameReleaseJSON(s.ScheduleAt, r.ScheduleAt) || !sameReleaseJSON(plan.ScheduleAt, r.ScheduleAt) ||
		!sameReleaseJSON(s.Lifecycle, r.Binding) {
		return model.ErrReleaseLifecycle
	}
	found := false
	for _, o := range r.Binding.TargetObjects {
		if o.ObjectID == r.Authority.ObjectID && o.TenantID == plan.TenantID && o.ServerID == plan.ServerID {
			found = true
		}
	}
	var result model.PlanInspectionResult
	if !found || json.Unmarshal([]byte(record.ResultJSON), &result) != nil || !result.Succeeded ||
		!sameReleaseJSON(result.Snapshot, s.ExpectedBefore) {
		return model.ErrReleaseLifecycle
	}
	digest, err := model.ReleaseApprovalDigest(s)
	if err != nil || digest != plan.Digest {
		return model.ErrReleaseLifecycle
	}
	return nil
}

func sameReleaseJSON(a, b any) bool {
	x, e := json.Marshal(a)
	y, f := json.Marshal(b)
	return e == nil && f == nil && string(x) == string(y)
}

func (store *Store) ApproveReleasePlan(ctx context.Context, id, actor, digest, confirmation string) (model.ReleasePlan, error) {
	return store.approveReleasePlan(ctx, id, actor, digest, confirmation, nil)
}

func (store *Store) ApprovePreparedReleasePlan(ctx context.Context, id string, authority model.ReleaseAuthority, request model.ApprovePlanRequest) (model.ReleasePlan, error) {
	return store.approveReleasePlan(ctx, id, authority.ActorHash, request.Digest, request.Confirmation, &authority)
}

func (store *Store) ReplayReleaseTask(ctx context.Context, plan model.ReleasePlan, actor, key string) (model.Task, error) {
	stored, err := store.GetReleasePlan(ctx, plan.ID)
	if err != nil {
		return model.Task{}, err
	}
	task, found, err := taskByIdempotency(ctx, store.db, key)
	if err != nil {
		return model.Task{}, err
	}
	if !found {
		return model.Task{}, model.ErrReleaseNotIntegrated
	}
	if task.ActorHash != actor || task.PlanID != stored.ID || task.PlanDigest != stored.Digest || stored.TaskID != task.ID ||
		plan.Digest != stored.Digest || task.RequestHash != HashConfirmation(stored.ID+"\x00"+stored.Digest) {
		return model.Task{}, ErrIdempotency
	}
	return task, nil
}

// 重用角色 Allows 与快照绑定语义，避免仅凭可构造的 authority 字段授予操作。
func validateReleaseObjectPermissionsTx(ctx context.Context, tx *sql.Tx, a model.ReleaseAuthority, objects []model.ReleaseTargetObject) error {
	policy, _, err := lifecyclePolicyTx(ctx, tx)
	if err != nil {
		return err
	}
	principal, ok := policy.Principals[a.ActorHash]
	if !ok {
		return ErrActorMismatch
	}
	now := time.Now()
	platform, roleAllowed, jitUsable := false, false, !principal.JIT
	for _, id := range principal.Roles {
		role, ok := policy.Roles[id]
		if ok {
			platform = platform || role.Allows("*")
			roleAllowed = roleAllowed || role.Allows(model.PermissionDeploy)
		}
	}
	for _, binding := range policy.Bindings {
		if binding.JIT && binding.Subject == a.ActorHash && (binding.TenantID == principal.TenantID || binding.TenantID == "*") && (binding.ExpiresAt == nil || now.Before(*binding.ExpiresAt)) {
			jitUsable = true
		}
	}
	if !jitUsable {
		return ErrActorMismatch
	}
	mainFound := false
	for _, object := range objects {
		mainFound = mainFound || object.ObjectID == a.ObjectID
		if object.TenantID != a.TenantID && !platform {
			return ErrActorMismatch
		}
		allowed := roleAllowed
		for _, binding := range policy.Bindings {
			subject := config.NormalizeAccessSubject(binding.Subject)
			if strings.Contains(subject, "@") {
				subject = config.AccessHashForEmail(subject)
			}
			if subject != a.ActorHash || (binding.TenantID != a.TenantID && binding.TenantID != "*") || (binding.ExpiresAt != nil && !now.Before(*binding.ExpiresAt)) {
				continue
			}
			matches := len(binding.ObjectIDs) == 0
			for _, id := range binding.ObjectIDs {
				matches = matches || id == object.ObjectID || id == "*"
			}
			role, ok := policy.Roles[binding.RoleID]
			allowed = allowed || (matches && ok && role.Allows(model.PermissionDeploy))
		}
		if !allowed {
			return ErrActorMismatch
		}
	}
	if !mainFound {
		return ErrActorMismatch
	}
	return nil
}
