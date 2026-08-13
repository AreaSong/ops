package runner

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGitHubAPIValidatorRequiresExpirationAndIssuesWrite(t *testing.T) {
	token := "github_pat_test"
	expiresAt := "2027-08-12T00:00:00Z"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+token {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		response.Header().Set("GitHub-Authentication-Token-Expiration", expiresAt)
		switch request.URL.Path {
		case "/user":
			_, _ = response.Write([]byte(`{"login":"operator"}`))
		case "/repos/AreaSong/ops":
			_, _ = response.Write([]byte(`{"full_name":"AreaSong/ops"}`))
		case "/repos/AreaSong/ops/issues":
			_, _ = response.Write([]byte(`[]`))
		case "/repos/AreaSong/ops/labels/alertmanager-critical":
			if request.Method != http.MethodPatch {
				http.Error(response, "wrong method", http.StatusMethodNotAllowed)
				return
			}
			_, _ = response.Write([]byte(`{"name":"alertmanager-critical"}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	validator := NewGitHubAPIValidator(server.URL)
	result, err := validator.Validate(context.Background(), token)
	expected, _ := time.Parse(time.RFC3339, expiresAt)
	if err != nil || !result.Expiration.Equal(expected) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestGitHubAPIValidatorFailsWithoutSignerExpiration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = fmt.Fprint(response, `{"login":"operator"}`)
	}))
	defer server.Close()
	if _, err := NewGitHubAPIValidator(server.URL).Validate(context.Background(), "token"); err == nil {
		t.Fatal("expected missing expiration header to fail closed")
	}
}

func TestParseGitHubTokenExpirationFormats(t *testing.T) {
	for _, value := range []string{"2027-08-12T00:00:00Z", "2027-08-12 00:00:00 UTC"} {
		if parsed := parseGitHubTokenExpiration(value); parsed.Format("2006-01-02") != "2027-08-12" {
			t.Fatalf("value=%q parsed=%v", value, parsed)
		}
	}
	if parsed := parseGitHubTokenExpiration("invalid"); !parsed.IsZero() {
		t.Fatalf("invalid expiration parsed=%v", parsed)
	}
}
