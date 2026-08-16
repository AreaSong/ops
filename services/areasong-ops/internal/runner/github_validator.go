package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type GitHubAPIValidator struct {
	baseURL string
	client  *http.Client
}

func NewGitHubAPIValidator(baseURL string) *GitHubAPIValidator {
	return &GitHubAPIValidator{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		client:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (validator *GitHubAPIValidator) Validate(
	ctx context.Context,
	token string,
) (GitHubCredentialValidation, error) {
	var user struct {
		Login string `json:"login"`
	}
	_, expiration, err := validator.request(ctx, http.MethodGet, "/user", token, "", &user)
	if err != nil || user.Login == "" {
		return GitHubCredentialValidation{}, errors.New("GitHub Token 身份验证失败")
	}
	if expiration.IsZero() {
		return GitHubCredentialValidation{}, errors.New("GitHub API 未返回 Token 到期时间")
	}
	var repository struct {
		FullName string `json:"full_name"`
	}
	if _, _, err := validator.request(ctx, http.MethodGet, "/repos/"+githubRepository, token, "", &repository); err != nil || repository.FullName != githubRepository {
		return GitHubCredentialValidation{}, errors.New("GitHub Token 无法读取固定仓库")
	}
	if _, _, err := validator.request(ctx, http.MethodGet,
		"/repos/"+githubRepository+"/issues?state=open&per_page=1", token, "", nil); err != nil {
		return GitHubCredentialValidation{}, errors.New("GitHub Token 缺少 Issues 读取能力")
	}
	_, _, err = validator.request(ctx, http.MethodPatch,
		"/repos/"+githubRepository+"/labels/alertmanager-critical", token,
		`{"new_name":"alertmanager-critical","color":"B60205"}`, nil)
	if err != nil {
		return GitHubCredentialValidation{}, errors.New("GitHub Token 缺少 Issues 写入能力")
	}
	return GitHubCredentialValidation{Expiration: expiration}, nil
}

func (validator *GitHubAPIValidator) Revoked(ctx context.Context, token string) (bool, error) {
	status, _, err := validator.request(ctx, http.MethodGet, "/user", token, "", nil)
	if status == http.StatusUnauthorized {
		return true, nil
	}
	if err != nil {
		return false, errors.New("无法确认旧 GitHub Token 是否已撤销")
	}
	return false, nil
}

func (validator *GitHubAPIValidator) request(
	ctx context.Context,
	method, path, token, body string,
	target any,
) (int, time.Time, error) {
	request, err := http.NewRequestWithContext(ctx, method, validator.baseURL+path, strings.NewReader(body))
	if err != nil {
		return 0, time.Time{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "areasong-ops-credential-rotation/1")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := validator.client.Do(request)
	if err != nil {
		return 0, time.Time{}, err
	}
	defer response.Body.Close()
	expiration := parseGitHubTokenExpiration(response.Header.Get("GitHub-Authentication-Token-Expiration"))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 32<<10))
		return response.StatusCode, expiration, fmt.Errorf("GitHub API 返回 HTTP %d", response.StatusCode)
	}
	if target == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 32<<10))
		return response.StatusCode, expiration, nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	if err := decoder.Decode(target); err != nil {
		return response.StatusCode, expiration, errors.New("GitHub API 响应无效")
	}
	return response.StatusCode, expiration, nil
}

func parseGitHubTokenExpiration(value string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05 MST"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed
		}
	}
	return time.Time{}
}
