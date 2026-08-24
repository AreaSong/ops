package web

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

type Session struct {
	Email    string `json:"email"`
	TenantID string `json:"tenantId"`
	Subject  string `json:"-"`
}

type Authenticator interface {
	Authenticate(*http.Request) (Session, error)
}

type accessClaims struct {
	Email string `json:"email"`
	jwt.RegisteredClaims
}

type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwksResponse struct {
	Keys []jwk `json:"keys"`
}

type AccessVerifier struct {
	issuer            string
	audience          string
	allowedEmail      string // legacy single-email test/config compatibility
	allowedIdentities IdentityDirectory
	client            *http.Client
	mu                sync.RWMutex
	keys              map[string]*rsa.PublicKey
	keysExpireAt      time.Time
}

type IdentityDirectory struct {
	tenants map[string]string
	emails  []string
}

var tenantIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)

func ParseIdentityDirectory(rawEmails, rawMappings, defaultTenant string) (IdentityDirectory, error) {
	directory := IdentityDirectory{tenants: make(map[string]string)}
	seen := make(map[string]struct{})
	for _, raw := range strings.Split(rawEmails, ",") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		email, err := canonicalEmail(raw)
		if err != nil {
			return IdentityDirectory{}, err
		}
		if _, exists := seen[email]; exists {
			return IdentityDirectory{}, fmt.Errorf("允许邮箱重复: %s", email)
		}
		seen[email] = struct{}{}
		directory.emails = append(directory.emails, email)
	}
	if len(directory.emails) == 0 {
		return IdentityDirectory{}, errors.New("Cloudflare Access 允许邮箱不能为空")
	}

	mappings := make(map[string]string)
	for _, raw := range strings.Split(rawMappings, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parts := strings.SplitN(raw, "=", 2)
		if len(parts) != 2 {
			return IdentityDirectory{}, fmt.Errorf("邮箱租户映射格式无效: %s", raw)
		}
		email, err := canonicalEmail(parts[0])
		if err != nil {
			return IdentityDirectory{}, err
		}
		tenantID := strings.ToLower(strings.TrimSpace(parts[1]))
		if !tenantIDPattern.MatchString(tenantID) {
			return IdentityDirectory{}, fmt.Errorf("邮箱 %s 的租户标识无效", email)
		}
		if existing, exists := mappings[email]; exists && existing != tenantID {
			return IdentityDirectory{}, fmt.Errorf("邮箱 %s 映射到多个租户", email)
		}
		mappings[email] = tenantID
	}
	defaultTenant = strings.ToLower(strings.TrimSpace(defaultTenant))
	if defaultTenant != "" && !tenantIDPattern.MatchString(defaultTenant) {
		return IdentityDirectory{}, errors.New("默认租户标识无效")
	}
	for _, email := range directory.emails {
		tenantID := mappings[email]
		if tenantID == "" {
			tenantID = defaultTenant
		}
		if tenantID == "" {
			return IdentityDirectory{}, fmt.Errorf("允许邮箱 %s 缺少租户映射", email)
		}
		directory.tenants[email] = tenantID
		delete(mappings, email)
	}
	if len(mappings) > 0 {
		return IdentityDirectory{}, errors.New("邮箱租户映射包含未在白名单中的邮箱")
	}
	return directory, nil
}

func (directory IdentityDirectory) Session(email string) (Session, bool) {
	email = config.NormalizeAccessSubject(email)
	tenantID, ok := directory.tenants[email]
	if !ok {
		return Session{}, false
	}
	return Session{Email: email, TenantID: tenantID, Subject: config.AccessHashForEmail(email)}, true
}

func (directory IdentityDirectory) FirstEmail() string {
	if len(directory.emails) == 0 {
		return ""
	}
	return directory.emails[0]
}

func canonicalEmail(value string) (string, error) {
	normalized := config.NormalizeAccessSubject(value)
	parsed, err := mail.ParseAddress(normalized)
	if err != nil || config.NormalizeAccessSubject(parsed.Address) != normalized {
		return "", fmt.Errorf("允许邮箱格式无效: %s", strings.TrimSpace(value))
	}
	return normalized, nil
}

func NewAccessVerifier(issuer, audience, allowedEmail string) (*AccessVerifier, error) {
	identities, err := ParseIdentityDirectory(allowedEmail, "", "default")
	if err != nil {
		return nil, err
	}
	return NewAccessVerifierWithIdentities(issuer, audience, identities)
}

func NewAccessVerifierWithIdentities(issuer, audience string, identities IdentityDirectory) (*AccessVerifier, error) {
	issuer = strings.TrimSuffix(strings.TrimSpace(issuer), "/")
	if !strings.HasPrefix(issuer, "https://") || strings.TrimSpace(audience) == "" || len(identities.tenants) == 0 {
		return nil, errors.New("Cloudflare Access 配置不完整")
	}
	return &AccessVerifier{
		issuer: issuer, audience: strings.TrimSpace(audience), allowedIdentities: identities,
		client: &http.Client{Timeout: 10 * time.Second}, keys: make(map[string]*rsa.PublicKey),
	}, nil
}

func (verifier *AccessVerifier) Authenticate(request *http.Request) (Session, error) {
	raw := strings.TrimSpace(request.Header.Get("Cf-Access-Jwt-Assertion"))
	if raw == "" {
		return Session{}, errors.New("缺少 Cloudflare Access JWT")
	}
	claims := &accessClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, verifier.keyFunc(request.Context()),
		jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(verifier.issuer),
		jwt.WithAudience(verifier.audience), jwt.WithExpirationRequired(), jwt.WithLeeway(30*time.Second))
	if err != nil || !token.Valid {
		return Session{}, errors.New("Cloudflare Access JWT 无效")
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	session, allowed := verifier.allowedIdentities.Session(email)
	if verifier.allowedEmail != "" {
		allowed = email == verifier.allowedEmail
		session = Session{Email: email, TenantID: "default", Subject: config.AccessHashForEmail(email)}
	}
	if !allowed {
		return Session{}, errors.New("邮箱不在控制面允许名单")
	}
	forwardedEmail := strings.ToLower(strings.TrimSpace(request.Header.Get("Cf-Access-Authenticated-User-Email")))
	if forwardedEmail != "" && forwardedEmail != email {
		return Session{}, errors.New("Cloudflare 身份头与 JWT 不一致")
	}
	return session, nil
}

func (verifier *AccessVerifier) keyFunc(ctx context.Context) jwt.Keyfunc {
	return func(token *jwt.Token) (any, error) {
		kid, ok := token.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, errors.New("JWT 缺少 kid")
		}
		if key := verifier.cachedKey(kid); key != nil {
			return key, nil
		}
		if err := verifier.refreshKeys(ctx); err != nil {
			return nil, err
		}
		if key := verifier.cachedKey(kid); key != nil {
			return key, nil
		}
		return nil, errors.New("JWT kid 不在 Cloudflare JWKS 中")
	}
}

func (verifier *AccessVerifier) cachedKey(kid string) *rsa.PublicKey {
	verifier.mu.RLock()
	defer verifier.mu.RUnlock()
	if time.Now().After(verifier.keysExpireAt) {
		return nil
	}
	return verifier.keys[kid]
}

func (verifier *AccessVerifier) refreshKeys(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, verifier.issuer+"/cdn-cgi/access/certs", nil)
	if err != nil {
		return err
	}
	response, err := verifier.client.Do(request)
	if err != nil {
		return fmt.Errorf("读取 Cloudflare JWKS 失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Cloudflare JWKS 返回 HTTP %d", response.StatusCode)
	}
	var payload jwksResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&payload); err != nil {
		return fmt.Errorf("解析 Cloudflare JWKS 失败: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, item := range payload.Keys {
		if item.Kty != "RSA" || item.Alg != "RS256" || item.Use != "sig" || item.Kid == "" {
			continue
		}
		key, err := rsaKey(item)
		if err != nil {
			continue
		}
		keys[item.Kid] = key
	}
	if len(keys) == 0 {
		return errors.New("Cloudflare JWKS 没有可用的 RS256 密钥")
	}
	verifier.mu.Lock()
	verifier.keys = keys
	verifier.keysExpireAt = time.Now().Add(time.Hour)
	verifier.mu.Unlock()
	return nil
}

func rsaKey(item jwk) (*rsa.PublicKey, error) {
	modulus, err := base64.RawURLEncoding.DecodeString(item.N)
	if err != nil {
		return nil, err
	}
	exponentBytes, err := base64.RawURLEncoding.DecodeString(item.E)
	if err != nil || len(exponentBytes) == 0 || len(exponentBytes) > 4 {
		return nil, errors.New("RSA exponent 无效")
	}
	exponent := 0
	for _, value := range exponentBytes {
		exponent = exponent<<8 + int(value)
	}
	if exponent < 3 || exponent%2 == 0 {
		return nil, errors.New("RSA exponent 无效")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: exponent}, nil
}

type DevelopmentAuthenticator struct {
	Email     string
	Directory IdentityDirectory
}

func (auth DevelopmentAuthenticator) Authenticate(_ *http.Request) (Session, error) {
	if auth.Email == "" {
		return Session{}, errors.New("开发身份未配置")
	}
	if len(auth.Directory.tenants) == 0 {
		email, err := canonicalEmail(auth.Email)
		if err != nil {
			return Session{}, err
		}
		return Session{Email: email, TenantID: "default", Subject: config.AccessHashForEmail(email)}, nil
	}
	session, ok := auth.Directory.Session(auth.Email)
	if !ok {
		return Session{}, errors.New("开发身份不在控制面允许名单")
	}
	return session, nil
}
