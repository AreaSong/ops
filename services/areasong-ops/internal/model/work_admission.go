package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

const WorkAdmissionKind = "release_plan_v1"
const (
	WorkAdmitted  = "admitted"
	WorkPreparing = "preparing"
	WorkTaskBound = "task_bound"
	WorkUncertain = "uncertain"
	WorkClosed    = "closed"
)

var ErrWorkAdmissionContract = errors.New("工作准入合同无效")

type WorkAdmissionTarget struct {
	TenantID           string `json:"tenantId"`
	ExpectedGeneration int64  `json:"expectedGeneration"`
}

// 目标完整性和批准真实性由可信调用方证明；此类型不授予执行权限。
type WorkAdmissionRequest struct {
	Version        int                   `json:"version"`
	Kind           string                `json:"kind"`
	WorkID         string                `json:"workId"`
	IdempotencyKey string                `json:"idempotencyKey"`
	ApprovalDigest string                `json:"approvalDigest"`
	ActorHash      string                `json:"actorHash"`
	Targets        []WorkAdmissionTarget `json:"targets"`
}

type WorkAdmission struct {
	ID                string
	Request           WorkAdmissionRequest
	RequestDigest     string
	State             string
	Revision          int64
	TaskID            string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ClosedAt          *time.Time
	CloseKind         string
	CloseDigest       string
	CloseEvidenceJSON string
}

func WorkDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func ValidWorkDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func NewWorkOwnerToken() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

// 排序/去重只处理相同身份，绝不修正租户ID或升级代次。
func CanonicalWorkRequest(input WorkAdmissionRequest) (WorkAdmissionRequest, string, string, error) {
	if input.Version != 1 || input.Kind != WorkAdmissionKind || !workIdentifier(input.WorkID) ||
		!workIdentifier(input.IdempotencyKey) || !ValidWorkDigest(input.ActorHash) ||
		!ValidWorkDigest(input.ApprovalDigest) || len(input.Targets) == 0 {
		return input, "", "", ErrWorkAdmissionContract
	}
	targets := make(map[string]int64)
	for _, target := range input.Targets {
		id := target.TenantID
		if !workIdentifier(id) || id == "*" || id != strings.ToLower(strings.TrimSpace(id)) || target.ExpectedGeneration <= 0 {
			return input, "", "", ErrWorkAdmissionContract
		}
		if generation, ok := targets[id]; ok && generation != target.ExpectedGeneration {
			return input, "", "", ErrWorkAdmissionContract
		}
		targets[id] = target.ExpectedGeneration
	}
	input.Targets = make([]WorkAdmissionTarget, 0, len(targets))
	for id, generation := range targets {
		input.Targets = append(input.Targets, WorkAdmissionTarget{id, generation})
	}
	sort.Slice(input.Targets, func(i, j int) bool { return input.Targets[i].TenantID < input.Targets[j].TenantID })
	raw, err := json.Marshal(input)
	if err != nil {
		return input, "", "", err
	}
	return input, string(raw), WorkDigest(string(raw)), nil
}

func workIdentifier(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsRune(value, '\x00')
}

// 外部事实由后续可信执行协调者核验，Store只验证此声明的格式与关联。
type WorkCloseEvidence struct {
	Kind             string `json:"kind"`
	ExecutorStopped  bool   `json:"executorStopped"`
	CleanupConfirmed bool   `json:"cleanupConfirmed"`
	ResultDigest     string `json:"resultDigest"`
	CleanupDigest    string `json:"cleanupDigest"`
}

func (e WorkCloseEvidence) Validate(closeKind string) error {
	switch closeKind {
	case "no_work":
		if e.Kind == "never_started_v1" && !e.ExecutorStopped && !e.CleanupConfirmed && e.ResultDigest == "" && e.CleanupDigest == "" {
			return nil
		}
	case "settled":
		if e.Kind == "execution_and_cleanup_settled_v1" && e.ExecutorStopped && e.CleanupConfirmed && ValidWorkDigest(e.ResultDigest) && ValidWorkDigest(e.CleanupDigest) {
			return nil
		}
	}
	return ErrWorkAdmissionContract
}
