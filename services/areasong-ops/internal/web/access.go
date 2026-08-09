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
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Session struct {
	Email string `json:"email"`
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
	issuer       string
	audience     string
	allowedEmail string
	client       *http.Client
	mu           sync.RWMutex
	keys         map[string]*rsa.PublicKey
	keysExpireAt time.Time
}

func NewAccessVerifier(issuer, audience, allowedEmail string) (*AccessVerifier, error) {
	issuer = strings.TrimSuffix(strings.TrimSpace(issuer), "/")
	if !strings.HasPrefix(issuer, "https://") || audience == "" || allowedEmail == "" {
		return nil, errors.New("Cloudflare Access 配置不完整")
	}
	return &AccessVerifier{
		issuer: issuer, audience: audience, allowedEmail: strings.ToLower(allowedEmail),
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
	if email != verifier.allowedEmail {
		return Session{}, errors.New("邮箱不在控制面允许名单")
	}
	forwardedEmail := strings.ToLower(strings.TrimSpace(request.Header.Get("Cf-Access-Authenticated-User-Email")))
	if forwardedEmail != "" && forwardedEmail != email {
		return Session{}, errors.New("Cloudflare 身份头与 JWT 不一致")
	}
	return Session{Email: email}, nil
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
	Email string
}

func (auth DevelopmentAuthenticator) Authenticate(_ *http.Request) (Session, error) {
	if auth.Email == "" {
		return Session{}, errors.New("开发身份未配置")
	}
	return Session{Email: strings.ToLower(auth.Email)}, nil
}
