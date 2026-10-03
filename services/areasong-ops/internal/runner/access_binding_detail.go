package runner

import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

var bindingIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// 白名单投影：身份为可关联的完整哈希，不携带主体、创建者或原始载荷。
type bindingReviewValue struct {
	ID              string             `json:"id"`
	Subject         string             `json:"subject"`
	TenantID        string             `json:"tenantId"`
	RoleID          string             `json:"roleId"`
	Permissions     []model.Permission `json:"permissions"`
	ObjectIDs       []string           `json:"objectIds"`
	ExpiresAt       *string            `json:"expiresAt"`
	JIT             bool               `json:"jit"`
	BindingApproval string             `json:"bindingApproval"`
}
type bindingReview struct {
	Before        *bindingReviewValue `json:"before"`
	After         *bindingReviewValue `json:"after"`
	ChangedFields []string            `json:"changedFields"`
}

func pureBindingRequest(r model.AccessControlUpdateRequest) bool {
	return len(r.Bindings)+len(r.RemoveBindingIDs) == 1 && len(r.Tenants) == 0 && len(r.RemoveTenantIDs) == 0 && len(r.Roles) == 0 && len(r.RemoveRoleIDs) == 0 && len(r.Principals) == 0 && len(r.RemovePrincipalSubjects) == 0 && r.Enforced == nil && r.Confirmation == "" && r.RequiresDualApproval
}

func (engine *Engine) projectBindingChange(ctx context.Context, detail accessChangeDetail, change model.AccessChange, r model.AccessControlUpdateRequest) accessChangeDetail {
	detail.Kind = "binding"
	if !pureBindingRequest(r) || !uuidPattern.MatchString(r.IdempotencyKey) || !change.RequiresDualApproval || change.ApprovalPolicy != model.ApprovalPolicyTwoParty || !actorPattern.MatchString(change.ActorHash) {
		detail.Reason = "unsupported_binding_request"
		return detail
	}
	detail.Availability = "unavailable"
	if r.ExpectedVersion <= 0 {
		return detail
	}
	baseline, found, err := engine.store.GetAccessPolicySnapshotVersion(ctx, r.ExpectedVersion)
	if err != nil || !found || baseline.Digest != digestText(baseline.PolicyJSON) {
		return detail
	}
	var policy config.AccessPolicy
	if json.Unmarshal([]byte(baseline.PolicyJSON), &policy) != nil {
		return detail
	}
	canonical, err := json.Marshal(&policy)
	if err != nil || string(canonical) != baseline.PolicyJSON {
		detail.Availability = "unsupported"
		detail.Reason = "incompatible_baseline"
		return detail
	}
	// 两次版本读取包围目标行检查；不能把多个只读查询宣称为原子快照。
	current, found, err := engine.store.GetAccessPolicySnapshot(ctx)
	if err != nil || !found {
		return detail
	}
	detail.CurrentVersion = current.Version
	if current.Version != baseline.Version {
		detail.Availability = "stale"
		return detail
	}
	review, operation, reason := engine.bindingReviewDiff(ctx, r, change.ActorHash, &policy)
	latest, found, err := engine.store.GetAccessPolicySnapshot(ctx)
	if err != nil || !found {
		return detail
	}
	detail.CurrentVersion = latest.Version
	if latest.Version != baseline.Version {
		detail.Availability = "stale"
		return detail
	}
	if review == nil {
		detail.Availability = "unsupported"
		detail.Reason = reason
		return detail
	}
	if change.State != model.AccessChangePendingApproval && change.State != model.AccessChangeApproved {
		return detail
	}
	detail.Availability = "ready"
	detail.Operation = operation
	detail.Binding = review
	return detail
}

func (engine *Engine) bindingReviewDiff(ctx context.Context, r model.AccessControlUpdateRequest, actor string, policy *config.AccessPolicy) (*bindingReview, string, string) {
	var after *bindingReviewValue
	var id string
	if len(r.Bindings) == 1 {
		raw := r.Bindings[0]
		if raw.CreatedBy != "" || !raw.CreatedAt.IsZero() || !raw.UpdatedAt.IsZero() {
			return nil, "", "binding_metadata"
		}
		normalized := normalizeAccessBinding(raw, actor)
		id = normalized.ID
		if engine.validateBinding(policy, normalized) != nil {
			return nil, "", "invalid_binding"
		}
		after = bindingValue(normalized, policy)
		if after == nil {
			return nil, "", "unsupported_binding"
		}
	} else {
		id = strings.TrimSpace(r.RemoveBindingIDs[0])
	}
	if !bindingIDPattern.MatchString(id) || id != strings.ToLower(id) {
		return nil, "", "invalid_binding_id"
	}
	var historical *model.RoleBinding
	for _, b := range policy.Bindings {
		if strings.EqualFold(strings.TrimSpace(b.ID), id) {
			if historical != nil || b.ID != id {
				return nil, "", "ambiguous_binding"
			}
			copy := b
			historical = &copy
		}
	}
	row, found, err := engine.store.GetBindingReviewTarget(ctx, id)
	if err != nil || (historical != nil) != found {
		return nil, "", "binding_source_mismatch"
	}
	var before *bindingReviewValue
	if historical != nil {
		before = bindingValue(*historical, policy)
		if before == nil || !actorPattern.MatchString(historical.CreatedBy) || !historical.CreatedAt.IsZero() || !historical.UpdatedAt.IsZero() || !actorPattern.MatchString(row.CreatedBy) || !reflect.DeepEqual(before, bindingValue(row, policy)) {
			return nil, "", "unsupported_binding_source"
		}
	}
	if before == nil && after == nil {
		return nil, "", "binding_not_found"
	}
	if before != nil && after != nil && (before.Subject != after.Subject || before.TenantID != after.TenantID) {
		return nil, "", "binding_identity_migration"
	}
	operation := "edit"
	if before == nil {
		operation = "create"
	} else if after == nil {
		operation = "revoke"
	}
	return &bindingReview{Before: before, After: after, ChangedFields: bindingChangedFields(before, after)}, operation, ""
}

func bindingValue(b model.RoleBinding, policy *config.AccessPolicy) *bindingReviewValue {
	if !bindingIDPattern.MatchString(b.ID) || !config.IsAccessHash(b.Subject) || b.Subject != canonicalAccessSubject(b.Subject) || !bindingIDPattern.MatchString(b.TenantID) || b.TenantID != strings.ToLower(strings.TrimSpace(b.TenantID)) || !tenantIsActive(policy, b.TenantID) || !ordinaryBinding(b) {
		return nil
	}
	role, ok := policy.Roles[b.RoleID]
	if !ok || role.ID != b.RoleID || !bindingIDPattern.MatchString(b.RoleID) || len(role.Permissions) == 0 {
		return nil
	}
	for _, p := range role.Permissions {
		if p != "*" && !registeredRolePermissions([]model.Permission{p}) {
			return nil
		}
	}
	// 未登记对象也可以精确表达；空字符串/控制字符等不可解释历史格式不支持。
	for _, id := range b.ObjectIDs {
		if id == "" || strings.IndexFunc(id, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
			return nil
		}
	}
	result := &bindingReviewValue{ID: b.ID, Subject: b.Subject, TenantID: b.TenantID, RoleID: b.RoleID, Permissions: append([]model.Permission{}, role.Permissions...), ObjectIDs: append([]string{}, b.ObjectIDs...), BindingApproval: "default"}
	if b.ExpiresAt != nil {
		value := b.ExpiresAt.UTC().Format(time.RFC3339Nano)
		result.ExpiresAt = &value
	}
	return result
}
func ordinaryBinding(b model.RoleBinding) bool {
	return !b.JIT && !b.RequiresDualApproval && b.ApprovalState == "" && b.ApprovedByHash == "" && b.SecondApprovedByHash == "" && b.ApprovedAt == nil && b.SecondApprovedAt == nil
}
func bindingChangedFields(before, after *bindingReviewValue) []string {
	fields := []string{"id", "subject", "tenantId", "roleId", "permissions", "objectIds", "expiresAt", "jit", "bindingApproval"}
	if before == nil || after == nil {
		return fields
	}
	changes := []string{}
	a, b := reflect.ValueOf(*before), reflect.ValueOf(*after)
	for i, key := range fields {
		if !reflect.DeepEqual(a.Field(i).Interface(), b.Field(i).Interface()) {
			changes = append(changes, key)
		}
	}
	return changes
}
