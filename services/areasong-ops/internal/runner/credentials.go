package runner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

const (
	githubRepository              = "AreaSong/ops"
	credentialTarget              = "Alertmanager GitHub Issue 同步"
	credentialConfirmation        = "轮换 GitHub 告警 Token"
	credentialClosureConfirmation = "我已撤销旧 GitHub Token"
)

type CurrentCredential struct {
	Configured  bool
	Fingerprint string
	ExpiresAt   string
}

type CredentialRotator interface {
	Current(context.Context) (CurrentCredential, error)
	Rotate(context.Context, string, string, string) (model.CredentialRotationResult, error)
	VerifyRevoked(context.Context, model.CredentialRotation) error
	RemoveRollback(context.Context, model.CredentialRotation) error
}

type GitHubCredentialValidation struct {
	Expiration time.Time
}

type GitHubCredentialValidator interface {
	Validate(context.Context, string) (GitHubCredentialValidation, error)
	Revoked(context.Context, string) (bool, error)
}

type CredentialSmoke interface {
	Run(context.Context, string) error
}

type unavailableCredentialRotator struct{}

func (unavailableCredentialRotator) Current(context.Context) (CurrentCredential, error) {
	return CurrentCredential{}, errors.New("凭据轮换当前不可用")
}

func (unavailableCredentialRotator) Rotate(
	context.Context, string, string, string,
) (model.CredentialRotationResult, error) {
	return failedCredentialResult("凭据轮换当前不可用"), errors.New("凭据轮换当前不可用")
}

func (unavailableCredentialRotator) VerifyRevoked(context.Context, model.CredentialRotation) error {
	return errors.New("凭据轮换当前不可用")
}

func (unavailableCredentialRotator) RemoveRollback(context.Context, model.CredentialRotation) error {
	return errors.New("凭据轮换当前不可用")
}

func (engine *Engine) CredentialProfile(ctx context.Context, actor string) (model.CredentialProfileView, error) {
	current, err := engine.credentials.Current(ctx)
	if err != nil {
		return model.CredentialProfileView{}, err
	}
	latest, found, err := engine.store.LatestCredentialRotation(ctx, model.GitHubAlertmanagerCredential)
	if err != nil {
		return model.CredentialProfileView{}, err
	}
	profile := model.CredentialProfileView{
		Type: model.GitHubAlertmanagerCredential, DisplayName: "GitHub 告警同步 Token",
		Target: credentialTarget, Repository: githubRepository, Risk: model.RiskHigh,
		ConfirmationPhrase: credentialConfirmation, Configured: current.Configured,
		CanManage:   engine.authorizePlatform(ctx, actor, model.PermissionManageConfig, "credentials") == nil,
		Fingerprint: current.Fingerprint, ExpiresAt: current.ExpiresAt,
	}
	if found {
		profile.LastRotation = &latest
	}
	return profile, nil
}

func (engine *Engine) RotateCredential(
	ctx context.Context,
	actorHash string,
	request model.CredentialRotationRequest,
) (model.CredentialRotation, bool, error) {
	engine.credentialMu.Lock()
	defer engine.credentialMu.Unlock()
	if !actorPattern.MatchString(actorHash) || request.CredentialType != model.GitHubAlertmanagerCredential ||
		!uuidPattern.MatchString(request.IdempotencyKey) {
		return model.CredentialRotation{}, false, errors.New("凭据轮换请求无效")
	}
	if err := engine.authorizePlatform(ctx, actorHash, model.PermissionManageConfig, "credentials"); err != nil {
		return model.CredentialRotation{}, false, err
	}
	if request.Confirmation != credentialConfirmation {
		return model.CredentialRotation{}, false, errors.New("凭据轮换确认短语不匹配")
	}
	if request.Secret == "" || len(request.Secret) > 512 {
		return model.CredentialRotation{}, false, errors.New("GitHub Token 格式无效")
	}
	if active, found, err := engine.store.LatestCredentialRotation(ctx, request.CredentialType); err != nil {
		return model.CredentialRotation{}, false, err
	} else if found && active.IdempotencyKey == request.IdempotencyKey {
		if active.ActorHash != actorHash || active.Fingerprint != credentialFingerprint(request.Secret) ||
			active.ExpiresAt != request.ExpiresAt {
			return model.CredentialRotation{}, false, store.ErrIdempotency
		}
		return active, false, nil
	} else if found && (active.State == model.CredentialRotationRunning ||
		active.State == model.CredentialRotationSwitchedPendingRevocation ||
		active.State == model.CredentialRotationRevocationVerified ||
		active.State == model.CredentialRotationNeedsAttention) {
		return model.CredentialRotation{}, false, fmt.Errorf("已有未收口的凭据轮换: %s", active.ID)
	}
	id, err := newUUID()
	if err != nil {
		return model.CredentialRotation{}, false, err
	}
	rotation := model.CredentialRotation{
		ID: id, IdempotencyKey: request.IdempotencyKey, ActorHash: actorHash,
		CredentialType: request.CredentialType, Target: credentialTarget,
		State: model.CredentialRotationRunning, Fingerprint: credentialFingerprint(request.Secret),
		ExpiresAt: request.ExpiresAt, CreatedAt: time.Now().UTC(),
	}
	rotation, created, err := engine.store.StartCredentialRotation(ctx, rotation)
	if err != nil || !created {
		return rotation, created, err
	}
	operationContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 100*time.Second)
	defer cancel()
	result, rotateErr := engine.credentials.Rotate(operationContext, rotation.ID, request.Secret, request.ExpiresAt)
	request.Secret = ""
	if err := engine.store.FinishCredentialRotation(operationContext, rotation.ID, result); err != nil {
		return model.CredentialRotation{}, true, err
	}
	rotation, err = engine.store.GetCredentialRotation(operationContext, rotation.ID)
	auditOutcome := "succeeded"
	if rotateErr != nil {
		auditOutcome = string(result.State)
	}
	_, auditErr := engine.store.AppendAudit(context.Background(), model.AuditEntry{
		ActorHash: actorHash, Event: "credential.rotation.finished",
		Resource: request.CredentialType, Outcome: auditOutcome,
		Detail: map[string]any{
			"target": credentialTarget, "fingerprint": rotation.Fingerprint,
			"expiresAt": rotation.ExpiresAt, "validation": result.ValidationResult,
			"rollback": result.RollbackResult,
		},
	})
	if err != nil {
		return model.CredentialRotation{}, true, err
	}
	if auditErr != nil {
		return model.CredentialRotation{}, true, auditErr
	}
	return rotation, true, rotateErr
}

func (engine *Engine) CloseCredentialRotation(
	ctx context.Context,
	actorHash, rotationID string,
	request model.CredentialRotationCloseRequest,
) (model.CredentialRotation, bool, error) {
	engine.credentialMu.Lock()
	defer engine.credentialMu.Unlock()
	if !actorPattern.MatchString(actorHash) || !uuidPattern.MatchString(rotationID) ||
		!uuidPattern.MatchString(request.IdempotencyKey) {
		return model.CredentialRotation{}, false, errors.New("凭据轮换收口请求无效")
	}
	if err := engine.authorizePlatform(ctx, actorHash, model.PermissionManageConfig, "credentials"); err != nil {
		return model.CredentialRotation{}, false, err
	}
	if request.Confirmation != credentialClosureConfirmation {
		return model.CredentialRotation{}, false, errors.New("凭据轮换收口确认短语不匹配")
	}
	rotation, err := engine.store.GetCredentialRotation(ctx, rotationID)
	if err != nil {
		return model.CredentialRotation{}, false, err
	}
	if rotation.State == model.CredentialRotationCompleted && rotation.ActorHash == actorHash {
		return rotation, false, nil
	}
	if rotation.ActorHash != actorHash {
		return model.CredentialRotation{}, false, errors.New("轮换收口必须由原操作者完成")
	}
	operationContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 40*time.Second)
	defer cancel()
	if rotation.State == model.CredentialRotationSwitchedPendingRevocation {
		if err := engine.credentials.VerifyRevoked(operationContext, rotation); err != nil {
			_, _ = engine.store.AppendAudit(context.Background(), model.AuditEntry{
				ActorHash: actorHash, Event: "credential.rotation.close_rejected",
				Resource: rotation.CredentialType, Outcome: "rejected",
				Detail: map[string]any{"rotationId": rotation.ID, "reason": redactText(err.Error())},
			})
			return model.CredentialRotation{}, false, err
		}
		rotation, err = engine.store.MarkCredentialRevocationVerified(
			operationContext, rotationID, actorHash, request.IdempotencyKey)
		if err != nil {
			return model.CredentialRotation{}, false, err
		}
	} else if rotation.State != model.CredentialRotationRevocationVerified {
		return model.CredentialRotation{}, false, errors.New("凭据轮换当前不能收口")
	}
	if err := engine.credentials.RemoveRollback(operationContext, rotation); err != nil {
		return model.CredentialRotation{}, false, err
	}
	rotation, closed, err := engine.store.CloseCredentialRotation(
		operationContext, rotationID, actorHash, rotation.ClosureIdempotencyKey, "旧凭据已验证撤销，轮换闭环完成")
	if err != nil {
		return model.CredentialRotation{}, false, err
	}
	if closed {
		_, err = engine.store.AppendAudit(context.Background(), model.AuditEntry{
			ActorHash: actorHash, Event: "credential.rotation.closed",
			Resource: rotation.CredentialType, Outcome: "succeeded",
			Detail: map[string]any{
				"rotationId": rotation.ID, "fingerprint": rotation.Fingerprint,
				"expiresAt": rotation.ExpiresAt, "oldCredential": "revoked",
			},
		})
	}
	return rotation, closed, err
}
