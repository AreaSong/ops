package runner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

// 不使用 Tenant 或 AccessControlView 序列化，保证新增字段不会隐式扩大投影。
type tenantReviewValue struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Status      string `json:"status"`
}

type accessChangeDetail struct {
	ReviewerHash    string                  `json:"reviewerHash"`
	ID              string                  `json:"id"`
	RequestDigest   string                  `json:"requestDigest"`
	State           model.AccessChangeState `json:"state"`
	ExpectedVersion int64                   `json:"expectedVersion"`
	CurrentVersion  int64                   `json:"currentVersion"`
	Kind            string                  `json:"kind"`
	Availability    string                  `json:"availability"`
	Operation       string                  `json:"operation,omitempty"`
	Before          *tenantReviewValue      `json:"before,omitempty"`
	After           *tenantReviewValue      `json:"after,omitempty"`
	Role            *roleReview             `json:"role,omitempty"`
	RoleDeletion    *roleDeletionReview     `json:"roleDeletion,omitempty"`
	Binding         *bindingReview          `json:"binding,omitempty"`
	Reason          string                  `json:"reason,omitempty"`
}

func (engine *Engine) AccessChangeDetail(ctx context.Context, actor, id string) (accessChangeDetail, error) {
	// access 是既有提案创建/批准使用的平台对象；租户角色本身不授予此权限。
	if err := engine.authorizePlatform(ctx, actor, model.PermissionManageAccess, "access"); err != nil {
		return accessChangeDetail{}, err
	}
	change, payload, err := engine.store.GetAccessChangeWithPayload(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return accessChangeDetail{}, store.ErrNotFound
	}
	if err != nil {
		return accessChangeDetail{}, err
	}
	detail := engine.projectAccessChange(ctx, change, payload)
	// 返回前重新校验当前权限；历史快照从不作为当前授权依据。
	if err := engine.authorizePlatform(ctx, actor, model.PermissionManageAccess, "access"); err != nil {
		return accessChangeDetail{}, err
	}
	detail.ReviewerHash = actor
	return detail, nil
}

func (engine *Engine) projectAccessChange(ctx context.Context, change model.AccessChange, payload string) accessChangeDetail {
	detail := accessChangeDetail{ID: change.ID, RequestDigest: change.RequestDigest, State: change.State, Kind: "unsupported", Availability: "unsupported"}
	var request model.AccessControlUpdateRequest
	if digestText(payload) != change.RequestDigest || json.Unmarshal([]byte(payload), &request) != nil {
		return detail
	}
	// 只接受当前服务端的确切编码；未知字段、重复键和不兼容历史格式均不作局部解释。
	canonical, err := json.Marshal(request)
	if err != nil || string(canonical) != payload || request.IdempotencyKey != change.IdempotencyKey {
		return detail
	}
	detail.ExpectedVersion = request.ExpectedVersion
	if len(request.Bindings) > 0 || len(request.RemoveBindingIDs) > 0 {
		return engine.projectBindingChange(ctx, detail, change, request)
	}
	if len(request.Tenants) == 0 && len(request.RemoveTenantIDs) == 0 && len(request.Roles) == 0 && len(request.RemoveRoleIDs) == 0 {
		detail.Kind = "other" // 原有非租户审批沿原流程，不声称有完整差异。
		return detail
	}
	if pureRoleRequest(request) || pureRoleDeletionRequest(request) {
		detail.Kind = "role"
		if pureRoleDeletionRequest(request) {
			detail.Kind = "role_deletion"
		}
		if !change.RequiresDualApproval || change.ApprovalPolicy != model.ApprovalPolicyTwoParty || !actorPattern.MatchString(change.ActorHash) {
			detail.Reason = "incompatible_approval"
			return detail
		}
	} else if pureTenantRequest(request) {
		detail.Kind = "tenant"
	} else {
		return detail
	}
	detail.Availability = "unavailable"
	if request.ExpectedVersion <= 0 {
		return detail
	}
	baseline, found, err := engine.store.GetAccessPolicySnapshotVersion(ctx, request.ExpectedVersion)
	if err != nil || !found || baseline.Digest != digestText(baseline.PolicyJSON) {
		return detail
	}
	var policy config.AccessPolicy
	if json.Unmarshal([]byte(baseline.PolicyJSON), &policy) != nil {
		return detail
	}
	var before, after *tenantReviewValue
	var role *roleReview
	var deletion *roleDeletionReview
	var operation string
	if detail.Kind == "role" || detail.Kind == "role_deletion" {
		// 历史策略有未知字段或重复键时，不能据部分解析宣称已完整审阅。
		canonicalPolicy, err := json.Marshal(&policy)
		if err != nil || string(canonicalPolicy) != baseline.PolicyJSON {
			detail.Availability, detail.Reason = "unsupported", "incompatible_baseline"
			return detail
		}
		if detail.Kind == "role_deletion" {
			deletion, detail.Reason = roleDeletionDiff(request.RemoveRoleIDs[0], &policy)
			operation = "delete"
		} else {
			role, operation, detail.Reason = roleReviewDiff(request.Roles[0], change.ActorHash, &policy)
		}
		if role == nil && deletion == nil {
			detail.Availability = "unsupported"
			return detail
		}
	} else {
		var supported bool
		before, after, operation, supported = tenantReviewDiff(request.Tenants[0], change.ActorHash, &policy)
		if !supported {
			detail.Availability = "unsupported"
			return detail
		}
	}

	current, found, err := engine.store.GetAccessPolicySnapshot(ctx)
	if err != nil || !found {
		return detail
	}
	detail.CurrentVersion = current.Version
	if current.Version != baseline.Version {
		detail.Availability = "stale"
		return detail
	}
	if change.State != model.AccessChangePendingApproval && change.State != model.AccessChangeApproved {
		return detail
	}
	detail.Availability, detail.Operation, detail.Before, detail.After = "ready", operation, before, after
	detail.Role = role
	detail.RoleDeletion = deletion
	return detail
}

func pureTenantRequest(request model.AccessControlUpdateRequest) bool {
	return len(request.Tenants) == 1 && len(request.Roles) == 0 && len(request.Bindings) == 0 && len(request.Principals) == 0 &&
		len(request.RemoveTenantIDs) == 0 && len(request.RemoveRoleIDs) == 0 && len(request.RemoveBindingIDs) == 0 &&
		len(request.RemovePrincipalSubjects) == 0 && request.Enforced == nil && request.Confirmation == "" && request.RequiresDualApproval
}

func tenantReviewDiff(raw model.Tenant, actor string, policy *config.AccessPolicy) (*tenantReviewValue, *tenantReviewValue, string, bool) {
	if raw.CreatedBy != "" || !raw.CreatedAt.IsZero() || !raw.UpdatedAt.IsZero() {
		return nil, nil, "", false
	}
	after, err := normalizeAccessTenant(raw, actor)
	if err != nil || after.Status != "active" || after.ID == policy.DefaultTenant {
		return nil, nil, "", false
	}
	next := &tenantReviewValue{ID: after.ID, DisplayName: after.DisplayName, Status: after.Status}
	before, exists := policy.Tenants[after.ID]
	if !exists {
		return nil, next, "create", true
	}
	if before.ID != after.ID || before.ID != strings.ToLower(strings.TrimSpace(before.ID)) || (before.CreatedBy == "" || before.CreatedBy == "bootstrap") || before.Status != "active" || before.DisplayName == after.DisplayName {
		return nil, nil, "", false
	}
	return &tenantReviewValue{ID: before.ID, DisplayName: before.DisplayName, Status: before.Status}, next, "rename", true
}

func (server *Server) accessChangeDetail(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	actor, ok := requireActor(response, request)
	if !ok {
		return
	}
	detail, err := server.engine.AccessChangeDetail(request.Context(), actor, request.PathValue("id"))
	if isAuthorizationError(err) {
		writeAuthorizationOrError(response, err, http.StatusForbidden)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(response, http.StatusNotFound, "访问策略变更不存在")
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "访问策略详情不可用")
		return
	}
	writeJSON(response, http.StatusOK, detail)
}
