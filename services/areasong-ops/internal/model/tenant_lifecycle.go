package model

import (
	"encoding/json"
	"errors"
	"strings"
)

const TenantLifecycleKind = "tenant_lifecycle_v1"

var ErrTenantLifecyclePayload = errors.New("普通访问策略入口不支持租户生命周期载荷")

// TenantLifecycle 只表示同一读取事务中的当前事实，不能回填历史快照。
type TenantLifecycle struct {
	TenantID      string `json:"tenantId"`
	Status        string `json:"status"`
	Generation    int64  `json:"generation"`
	PolicyVersion int64  `json:"policyVersion"`
}

// 独立编码避免给旧请求、快照或审批摘要隐式增加字段。
type TenantLifecycleTransitionRequest struct {
	Kind                 string `json:"kind"`
	TenantID             string `json:"tenantId"`
	ExpectedStatus       string `json:"expectedStatus"`
	ExpectedGeneration   int64  `json:"expectedGeneration"`
	TargetStatus         string `json:"targetStatus"`
	ExpectedVersion      int64  `json:"expectedVersion"`
	RequiresDualApproval bool   `json:"requiresDualApproval"`
	IdempotencyKey       string `json:"idempotencyKey"`
}

func DecodeTenantLifecycleTransition(payload string) (TenantLifecycleTransitionRequest, error) {
	var request TenantLifecycleTransitionRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return request, err
	}
	canonical, err := json.Marshal(request)
	if err != nil || string(canonical) != payload {
		return request, errors.New("租户生命周期载荷编码不兼容")
	}
	if request.Kind != TenantLifecycleKind || request.TenantID == "" ||
		request.TenantID != strings.ToLower(strings.TrimSpace(request.TenantID)) ||
		request.ExpectedGeneration <= 0 || request.ExpectedVersion <= 0 ||
		!request.RequiresDualApproval || request.IdempotencyKey == "" {
		return request, errors.New("租户生命周期请求信息无效")
	}
	if !((request.ExpectedStatus == "active" && request.TargetStatus == "disabled") ||
		(request.ExpectedStatus == "disabled" && request.TargetStatus == "active")) {
		return request, errors.New("租户生命周期仅支持显式停用或启用")
	}
	return request, nil
}

// 普通历史解码保持原合同，但不能把生命周期或混合载荷忽略成空变更。
func RejectTenantLifecyclePayload(payload string) error {
	var value any
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		return err
	}
	if containsTenantLifecycleField(value, true) {
		return ErrTenantLifecyclePayload
	}
	return nil
}

func containsTenantLifecycleField(value any, root bool) bool {
	switch item := value.(type) {
	case map[string]any:
		for name, child := range item {
			switch strings.ToLower(name) {
			case "kind", "expectedstatus", "expectedgeneration", "targetstatus", "lifecyclegeneration":
				return true
			case "tenantid":
				if root {
					return true
				}
			}
			// 快照的实体集合按 ID 索引；ID 是数据，不能当成协议字段。
			if root && (strings.EqualFold(name, "tenants") || strings.EqualFold(name, "roles") || strings.EqualFold(name, "principals")) {
				if entities, ok := child.(map[string]any); ok {
					for _, entity := range entities {
						if containsTenantLifecycleField(entity, false) {
							return true
						}
					}
					continue
				}
			}
			if containsTenantLifecycleField(child, false) {
				return true
			}
		}
	case []any:
		for _, child := range item {
			if containsTenantLifecycleField(child, false) {
				return true
			}
		}
	}
	return false
}
