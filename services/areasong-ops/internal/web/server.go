package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/AreaSong/ops/services/areasong-ops/internal/buildinfo"
)

const (
	actorHeader = "X-AreaSong-Ops-Actor-Hash"
	csrfCookie  = "areasong_ops_csrf"
)

type Server struct {
	auth          Authenticator
	runner        *RunnerClient
	publicOrigin  string
	externalLinks ExternalLinks
	development   bool
	assets        fs.FS
}

type ServerOptions struct {
	PublicOrigin string
	GrafanaURL   string
	Development  bool
}

type ExternalLinks struct {
	Grafana string `json:"grafana,omitempty"`
	Alerts  string `json:"alerts,omitempty"`
}

func NewServer(
	auth Authenticator,
	runner *RunnerClient,
	options ServerOptions,
	assets embed.FS,
) (http.Handler, error) {
	static, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}
	links, err := buildExternalLinks(options.GrafanaURL, options.Development)
	if err != nil {
		return nil, err
	}
	server := &Server{
		auth: auth, runner: runner, publicOrigin: strings.TrimSuffix(options.PublicOrigin, "/"),
		externalLinks: links, development: options.Development, assets: static,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.health)
	mux.HandleFunc("GET /metrics", server.metrics)
	mux.HandleFunc("GET /api/session", server.session)
	mux.HandleFunc("/api/", server.api)
	mux.HandleFunc("/", server.static)
	return server.securityHeaders(mux), nil
}

func (server *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(response, request)
	})
}

func (server *Server) health(response http.ResponseWriter, request *http.Request) {
	up := server.runnerRequest(request.Context(), http.MethodGet, "/healthz", "", nil, false)
	if up != nil {
		defer up.Body.Close()
	}
	status := http.StatusOK
	if up == nil || up.StatusCode != http.StatusOK {
		status = http.StatusServiceUnavailable
	}
	writeJSON(response, status, map[string]any{
		"ok": status == http.StatusOK, "component": "web",
		"version": buildinfo.Version, "revision": buildinfo.Revision,
	})
}

func (server *Server) session(response http.ResponseWriter, request *http.Request) {
	session, err := server.auth.Authenticate(request)
	if err != nil {
		writeJSON(response, http.StatusUnauthorized, map[string]any{"error": err.Error()})
		return
	}
	token, err := randomToken()
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]any{"error": "无法创建 CSRF 令牌"})
		return
	}
	http.SetCookie(response, &http.Cookie{
		Name: csrfCookie, Value: token, Path: "/", Secure: !server.development,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 1800,
	})
	writeJSON(response, http.StatusOK, map[string]any{
		"email": session.Email, "csrfToken": token, "links": server.externalLinks,
	})
}

func buildExternalLinks(rawURL string, development bool) (ExternalLinks, error) {
	if rawURL == "" {
		return ExternalLinks{}, nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ExternalLinks{}, errors.New("Grafana URL 无效")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return ExternalLinks{}, errors.New("Grafana URL 必须是 origin")
	}
	if parsed.Scheme != "https" && !(development && parsed.Scheme == "http") {
		return ExternalLinks{}, errors.New("Grafana URL 必须使用 HTTPS")
	}
	origin := strings.TrimSuffix(parsed.String(), "/")
	return ExternalLinks{Grafana: origin, Alerts: origin + "/alerting/list"}, nil
}

func (server *Server) api(response http.ResponseWriter, request *http.Request) {
	session, err := server.auth.Authenticate(request)
	if err != nil {
		writeJSON(response, http.StatusUnauthorized, map[string]any{"error": err.Error()})
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		if err := server.verifyMutation(request); err != nil {
			writeJSON(response, http.StatusForbidden, map[string]any{"error": err.Error()})
			return
		}
	}
	runnerPath := "/v1/" + strings.TrimPrefix(request.URL.Path, "/api/")
	if strings.Contains(runnerPath, "..") {
		writeJSON(response, http.StatusBadRequest, map[string]any{"error": "API 路径无效"})
		return
	}
	actor := sha256.Sum256([]byte(strings.ToLower(session.Email)))
	actorHash := hex.EncodeToString(actor[:])
	stream := runnerPath == "/v1/events"
	contextWithActor := context.WithValue(request.Context(), actorContextKey{}, actorHash)
	upstream := server.runnerRequest(contextWithActor, request.Method, runnerPath, request.URL.RawQuery, request.Body, stream)
	if upstream == nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]any{"error": "Runner 当前不可用"})
		return
	}
	defer upstream.Body.Close()
	copyHeader(response.Header(), upstream.Header, "Content-Type")
	copyHeader(response.Header(), upstream.Header, "Cache-Control")
	if stream {
		response.Header().Set("X-Accel-Buffering", "no")
	}
	response.WriteHeader(upstream.StatusCode)
	if stream {
		streamBody(response, upstream.Body)
		return
	}
	_, _ = io.CopyN(response, upstream.Body, 2<<20)
}

func (server *Server) runnerRequest(
	ctx context.Context,
	method, requestPath, rawQuery string,
	body io.Reader,
	stream bool,
) *http.Response {
	request, err := http.NewRequestWithContext(ctx, method, "http://runner"+requestPath, body)
	if err != nil {
		return nil
	}
	request.URL.RawQuery = rawQuery
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if actor, ok := ctx.Value(actorContextKey{}).(string); ok {
		request.Header.Set(actorHeader, actor)
	}
	client := server.runner.normal
	if strings.HasPrefix(requestPath, "/v1/credentials/") ||
		strings.HasPrefix(requestPath, "/v1/credential-rotations/") {
		client = server.runner.credential
	}
	if stream {
		client = server.runner.stream
	}
	response, err := client.Do(request)
	if err != nil {
		return nil
	}
	return response
}

type actorContextKey struct{}

func (server *Server) verifyMutation(request *http.Request) error {
	if !server.development && request.Header.Get("Origin") != server.publicOrigin {
		return errors.New("请求来源不匹配")
	}
	cookie, err := request.Cookie(csrfCookie)
	if err != nil {
		return errors.New("缺少 CSRF Cookie")
	}
	header := request.Header.Get("X-AreaSong-Ops-CSRF")
	if len(cookie.Value) != len(header) || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) != 1 {
		return errors.New("CSRF 令牌无效")
	}
	return nil
}

func (server *Server) static(response http.ResponseWriter, request *http.Request) {
	assetPath := strings.TrimPrefix(path.Clean(request.URL.Path), "/")
	if assetPath == "." || assetPath == "" {
		assetPath = "index.html"
	}
	data, err := fs.ReadFile(server.assets, assetPath)
	if err != nil && !strings.Contains(path.Base(assetPath), ".") {
		data, err = fs.ReadFile(server.assets, "index.html")
		assetPath = "index.html"
	}
	if err != nil {
		http.NotFound(response, request)
		return
	}
	if contentType := mime.TypeByExtension(path.Ext(assetPath)); contentType != "" {
		response.Header().Set("Content-Type", contentType)
	}
	if strings.HasPrefix(assetPath, "assets/") {
		response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		response.Header().Set("Cache-Control", "no-cache")
	}
	_, _ = response.Write(data)
}

func streamBody(response http.ResponseWriter, body io.Reader) {
	flusher, ok := response.(http.Flusher)
	buffer := make([]byte, 16<<10)
	for {
		count, err := body.Read(buffer)
		if count > 0 {
			_, _ = response.Write(buffer[:count])
			if ok {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func copyHeader(target http.Header, source http.Header, name string) {
	if value := source.Get(name); value != "" {
		target.Set(name, value)
	}
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
