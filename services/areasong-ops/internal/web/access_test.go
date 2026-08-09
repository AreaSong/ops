package web

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestAccessVerifierValidatesJWTAndEmail(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	kid := "test-key"
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/cdn-cgi/access/certs" {
			http.NotFound(response, request)
			return
		}
		exponent := big.NewInt(int64(privateKey.PublicKey.E)).Bytes()
		_ = json.NewEncoder(response).Encode(jwksResponse{Keys: []jwk{{
			Kid: kid, Kty: "RSA", Alg: "RS256", Use: "sig",
			N: base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes()),
			E: base64.RawURLEncoding.EncodeToString(exponent),
		}}})
	}))
	defer server.Close()
	verifier, err := NewAccessVerifier(server.URL, "audience", "song80184@gmail.com")
	if err != nil {
		t.Fatal(err)
	}
	verifier.client = server.Client()
	signed := signAccessToken(t, privateKey, kid, server.URL, "song80184@gmail.com")
	request := httptest.NewRequest(http.MethodGet, "https://ops.areasong.top/api/session", nil)
	request.Header.Set("Cf-Access-Jwt-Assertion", signed)
	request.Header.Set("Cf-Access-Authenticated-User-Email", "song80184@gmail.com")
	session, err := verifier.Authenticate(request)
	if err != nil || session.Email != "song80184@gmail.com" {
		t.Fatalf("session=%+v err=%v", session, err)
	}

	request.Header.Set("Cf-Access-Authenticated-User-Email", "other@example.com")
	if _, err := verifier.Authenticate(request); err == nil {
		t.Fatal("expected mismatched forwarded email to be rejected")
	}
}

func TestAccessVerifierRejectsSignedButDisallowedEmail(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	verifier := &AccessVerifier{
		issuer: "https://team.cloudflareaccess.com", audience: "audience",
		allowedEmail: "song80184@gmail.com", keys: map[string]*rsa.PublicKey{"key": &privateKey.PublicKey},
		keysExpireAt: time.Now().Add(time.Hour), client: http.DefaultClient,
	}
	request := httptest.NewRequest(http.MethodGet, "https://ops.areasong.top/api/session", nil)
	request.Header.Set("Cf-Access-Jwt-Assertion", signAccessToken(
		t, privateKey, "key", verifier.issuer, "attacker@example.com"))
	if _, err := verifier.Authenticate(request); err == nil {
		t.Fatal("expected disallowed email to be rejected")
	}
}

func signAccessToken(t *testing.T, key *rsa.PrivateKey, kid, issuer, email string) string {
	t.Helper()
	claims := accessClaims{
		Email: email,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: issuer, Audience: jwt.ClaimStrings{"audience"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}
