package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type fakeCredentialValidator struct {
	validation  GitHubCredentialValidation
	validateErr error
	revoked     bool
	revokeErr   error
}

func (validator fakeCredentialValidator) Validate(context.Context, string) (GitHubCredentialValidation, error) {
	return validator.validation, validator.validateErr
}

func (validator fakeCredentialValidator) Revoked(context.Context, string) (bool, error) {
	return validator.revoked, validator.revokeErr
}

type fakeCredentialSmoke struct {
	configPath string
	metricPath string
	failToken  string
	failAlways bool
}

func (smoke fakeCredentialSmoke) Run(_ context.Context, path string) error {
	if smoke.failAlways {
		return errors.New("controlled persistent smoke failure")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if smoke.failToken != "" && strings.Contains(string(content), smoke.failToken) {
		return errors.New("controlled smoke failure")
	}
	values := parseTestConfig(string(content))
	expiry, err := time.Parse("2006-01-02", values["GITHUB_TOKEN_EXPIRES_AT"])
	if err != nil {
		return err
	}
	metrics := "alertmanager_github_token_expiry_configured 1\n" +
		"alertmanager_github_token_expiry_timestamp " + formatInt(expiry.Unix()) + "\n" +
		"alertmanager_github_issue_sync_success 1\n"
	return os.WriteFile(smoke.metricPath, []byte(metrics), 0o644)
}

func TestCredentialRotationSwitchesAndClosesOnlyAfterRevocation(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "credentials", "alertmanager-github.env")
	metricPath := filepath.Join(root, "metrics.prom")
	writeTestCredential(t, configPath, "old-token-abcdefghijklmnopqrstuvwxyz123456", "2026-12-31", metricPath)
	validator := &fakeCredentialValidator{
		validation: GitHubCredentialValidation{Expiration: time.Date(2027, 8, 12, 0, 0, 0, 0, time.UTC)},
	}
	rotator := NewGitHubCredentialRotator(GitHubCredentialRotatorOptions{
		ConfigPath: configPath, RollbackRoot: filepath.Join(root, "rollbacks"), MetricPath: metricPath,
		Validator: validator, Smoke: fakeCredentialSmoke{metricPath: metricPath},
		Now: func() time.Time { return time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC) }, AllowTestToken: true,
	})
	newToken := "new-token-abcdefghijklmnopqrstuvwxyz123456"
	result, err := rotator.Rotate(context.Background(), "rotation-one", newToken, "2027-08-12")
	if err != nil || result.State != model.CredentialRotationSwitchedPendingRevocation {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	content, err := os.ReadFile(configPath)
	if err != nil || !strings.Contains(string(content), newToken) {
		t.Fatalf("new credential was not switched: err=%v", err)
	}
	if info, err := os.Stat(configPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode=%v err=%v", info.Mode().Perm(), err)
	}
	rotation := model.CredentialRotation{ID: "rotation-one", Fingerprint: credentialFingerprint(newToken)}
	if err := rotator.VerifyRevoked(context.Background(), rotation); err == nil {
		t.Fatal("expected valid old token to block closure")
	}
	validator.revoked = true
	if err := rotator.VerifyRevoked(context.Background(), rotation); err != nil {
		t.Fatal(err)
	}
	if err := rotator.RemoveRollback(context.Background(), rotation); err != nil {
		t.Fatal(err)
	}
	if err := rotator.RemoveRollback(context.Background(), rotation); err != nil {
		t.Fatalf("idempotent rollback removal failed: %v", err)
	}
	if _, err := os.Stat(rotator.rollbackPath(rotation.ID)); !os.IsNotExist(err) {
		t.Fatalf("rollback copy was not removed: %v", err)
	}
}

func TestCredentialRotationValidationFailureDoesNotChangeConfig(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "credentials", "alertmanager-github.env")
	metricPath := filepath.Join(root, "metrics.prom")
	oldToken := "old-token-abcdefghijklmnopqrstuvwxyz123456"
	writeTestCredential(t, configPath, oldToken, "2026-12-31", metricPath)
	rotator := NewGitHubCredentialRotator(GitHubCredentialRotatorOptions{
		ConfigPath: configPath, RollbackRoot: filepath.Join(root, "rollbacks"), MetricPath: metricPath,
		Validator: fakeCredentialValidator{validateErr: errors.New("scope rejected")},
		Smoke:     fakeCredentialSmoke{metricPath: metricPath},
		Now:       func() time.Time { return time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC) }, AllowTestToken: true,
	})
	result, err := rotator.Rotate(context.Background(), "rotation-two",
		"new-token-abcdefghijklmnopqrstuvwxyz123456", "2027-08-12")
	if err == nil || result.State != model.CredentialRotationFailed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	content, _ := os.ReadFile(configPath)
	if !strings.Contains(string(content), oldToken) || strings.Contains(string(content), "new-token") {
		t.Fatal("validation failure changed production config")
	}
}

func TestCredentialRotationRejectsSignerExpirationMismatch(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "credentials", "alertmanager-github.env")
	metricPath := filepath.Join(root, "metrics.prom")
	oldToken := "old-token-abcdefghijklmnopqrstuvwxyz123456"
	writeTestCredential(t, configPath, oldToken, "2026-12-31", metricPath)
	rotator := NewGitHubCredentialRotator(GitHubCredentialRotatorOptions{
		ConfigPath: configPath, RollbackRoot: filepath.Join(root, "rollbacks"), MetricPath: metricPath,
		Validator: fakeCredentialValidator{
			validation: GitHubCredentialValidation{Expiration: time.Date(2027, 8, 11, 0, 0, 0, 0, time.UTC)},
		},
		Smoke: fakeCredentialSmoke{metricPath: metricPath},
		Now:   func() time.Time { return time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC) }, AllowTestToken: true,
	})
	result, err := rotator.Rotate(context.Background(), "rotation-expiry",
		"new-token-abcdefghijklmnopqrstuvwxyz123456", "2027-08-12")
	if err == nil || result.State != model.CredentialRotationFailed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	content, _ := os.ReadFile(configPath)
	if !strings.Contains(string(content), oldToken) {
		t.Fatal("expiration mismatch changed production config")
	}
}

func TestCredentialRotationSmokeFailureRestoresOldConfig(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "credentials", "alertmanager-github.env")
	metricPath := filepath.Join(root, "metrics.prom")
	oldToken := "old-token-abcdefghijklmnopqrstuvwxyz123456"
	newToken := "new-token-abcdefghijklmnopqrstuvwxyz123456"
	writeTestCredential(t, configPath, oldToken, "2026-12-31", metricPath)
	rotator := NewGitHubCredentialRotator(GitHubCredentialRotatorOptions{
		ConfigPath: configPath, RollbackRoot: filepath.Join(root, "rollbacks"), MetricPath: metricPath,
		Validator: fakeCredentialValidator{
			validation: GitHubCredentialValidation{Expiration: time.Date(2027, 8, 12, 0, 0, 0, 0, time.UTC)},
		},
		Smoke: fakeCredentialSmoke{metricPath: metricPath, failToken: newToken},
		Now:   func() time.Time { return time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC) }, AllowTestToken: true,
	})
	result, err := rotator.Rotate(context.Background(), "rotation-three", newToken, "2027-08-12")
	if err == nil || result.State != model.CredentialRotationRolledBack {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	content, _ := os.ReadFile(configPath)
	if !strings.Contains(string(content), oldToken) || strings.Contains(string(content), newToken) {
		t.Fatal("smoke failure did not restore old credential")
	}
}

func TestCredentialRotationKeepsRollbackCopyWhenRestoredSmokeFails(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "credentials", "alertmanager-github.env")
	metricPath := filepath.Join(root, "metrics.prom")
	oldToken := "old-token-abcdefghijklmnopqrstuvwxyz123456"
	writeTestCredential(t, configPath, oldToken, "2026-12-31", metricPath)
	rotator := NewGitHubCredentialRotator(GitHubCredentialRotatorOptions{
		ConfigPath: configPath, RollbackRoot: filepath.Join(root, "rollbacks"), MetricPath: metricPath,
		Validator: fakeCredentialValidator{
			validation: GitHubCredentialValidation{Expiration: time.Date(2027, 8, 12, 0, 0, 0, 0, time.UTC)},
		},
		Smoke: fakeCredentialSmoke{metricPath: metricPath, failAlways: true},
		Now:   func() time.Time { return time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC) }, AllowTestToken: true,
	})
	result, err := rotator.Rotate(context.Background(), "rotation-four",
		"new-token-abcdefghijklmnopqrstuvwxyz123456", "2027-08-12")
	if err == nil || result.State != model.CredentialRotationNeedsAttention {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(rotator.rollbackPath("rotation-four")); err != nil {
		t.Fatalf("rollback copy was not retained: %v", err)
	}
}

func TestRedactTextRemovesBareGitHubTokens(t *testing.T) {
	for _, token := range []string{
		"github_pat_1234567890abcdefghijklmnopqrstuvwxyz",
		"ghp_1234567890abcdefghijklmnopqrstuvwxyz",
	} {
		redacted := redactText("command output: " + token)
		if strings.Contains(redacted, token) || !strings.Contains(redacted, "[REDACTED]") {
			t.Fatalf("token was not redacted: %q", redacted)
		}
	}
}

func TestCredentialMetricRequiresExactValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential.prom")
	expiry, _ := time.Parse("2006-01-02", "2027-08-12")
	content := "alertmanager_github_token_expiry_configured 1\n" +
		"alertmanager_github_token_expiry_timestamp " + formatInt(expiry.Unix()) + "\n" +
		"alertmanager_github_issue_sync_success 10\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyCredentialMetric(path, "2027-08-12"); err == nil {
		t.Fatal("non-exact sync success metric was accepted")
	}
}

func TestCredentialConfigRejectsOversizedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "credential.env")
	metricPath := filepath.Join(root, "metric.prom")
	content := append(renderCredentialConfig(
		"old-token-abcdefghijklmnopqrstuvwxyz123456", "2027-08-12", metricPath),
		[]byte("#"+strings.Repeat("x", 17<<10)+"\n")...,
	)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	rotator := NewGitHubCredentialRotator(GitHubCredentialRotatorOptions{MetricPath: metricPath})
	if _, err := rotator.readConfig(path); err == nil {
		t.Fatal("oversized credential file was accepted")
	}
}

func TestCredentialSmokeAcquiresLegacyThenManagedLock(t *testing.T) {
	command := newCredentialSmokeCommand(context.Background(), "/credential.env")
	joined := strings.Join(command.Args, " ")
	legacyOffset := strings.Index(joined, legacyCredentialLockPath)
	managedOffset := strings.Index(joined, managedCredentialLockPath)
	if legacyOffset < 0 || managedOffset < 0 || legacyOffset >= managedOffset {
		t.Fatalf("unexpected credential smoke lock order: %v", command.Args)
	}
}

func writeTestCredential(t *testing.T, path, token, expiresAt, metricPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, renderCredentialConfig(token, expiresAt, metricPath), 0o600); err != nil {
		t.Fatal(err)
	}
}

func parseTestConfig(content string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			result[key] = value
		}
	}
	return result
}

func formatInt(value int64) string {
	return fmt.Sprintf("%d", value)
}
