package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

type KubernetesResourceIdentity struct {
	APIVersion      string `json:"apiVersion"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	Exists          bool   `json:"exists"`
	UID             string `json:"uid,omitempty"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
}

type KubernetesPreview struct {
	Version            int                          `json:"version"`
	PolicyDigest       string                       `json:"policyDigest"`
	ClusterFingerprint string                       `json:"clusterFingerprint"`
	Diff               string                       `json:"diff"`
	DiffDigest         string                       `json:"diffDigest"`
	HasChanges         bool                         `json:"hasChanges"`
	Resources          []KubernetesResourceIdentity `json:"resources"`
	SourceOperationID  string                       `json:"sourceOperationId,omitempty"`
	ObservedAt         time.Time                    `json:"observedAt"`
	ExpiresAt          time.Time                    `json:"expiresAt"`
}

// 清单 hash 不是批准 hash；后者还绑定预览、目标及回滚来源。
func (plan KubernetesPlan) ApprovalDigest() (string, error) {
	if plan.Preview == nil || plan.Preview.Version != 1 || plan.Preview.PolicyDigest == "" ||
		plan.Preview.ClusterFingerprint == "" || plan.Preview.DiffDigest == "" ||
		len(plan.Preview.Resources) == 0 || plan.Preview.ObservedAt.IsZero() ||
		!plan.Preview.ExpiresAt.After(plan.Preview.ObservedAt) {
		return "", errors.New("Kubernetes 计划缺少完整预览证据，请重新创建")
	}
	payload := struct {
		Actor, Tenant, Action, Manifest, SourcePlan, TargetPlan, SourceManifest, ApprovalPolicy string
		Target                                                                                  KubernetesTarget
		Preview                                                                                 *KubernetesPreview
	}{plan.ActorHash, plan.TenantID, plan.Action, plan.ManifestDigest, plan.RollbackOfPlanID,
		plan.RollbackTargetPlanID, plan.SourceManifestDigest, plan.ApprovalPolicy, plan.Target, plan.Preview}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
