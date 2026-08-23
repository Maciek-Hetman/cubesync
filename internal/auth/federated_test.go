package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Maciek-Hetman/cubing-sync-backend/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

func TestParseBooleanClaim(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		raw  string
		want bool
	}{
		"boolean true":  {raw: `true`, want: true},
		"string true":   {raw: `"true"`, want: true},
		"boolean false": {raw: `false`, want: false},
		"invalid":       {raw: `"yes"`, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := parseBooleanClaim(json.RawMessage(test.raw)); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestAppleClientSecret(t *testing.T) {
	t.Parallel()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	verifier := NewOIDCVerifier(config.Config{
		AppleTeamID: "TEAM123", AppleKeyID: "KEY123",
		ApplePrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})),
	})
	raw, err := verifier.appleClientSecret("com.example.cubetimer")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.Parse(raw, func(token *jwt.Token) (any, error) {
		return &key.PublicKey, nil
	}, jwt.WithIssuer("TEAM123"), jwt.WithAudience("https://appleid.apple.com"))
	if err != nil || !parsed.Valid {
		t.Fatalf("Apple client secret is invalid: %v", err)
	}
	if parsed.Header["kid"] != "KEY123" {
		t.Fatalf("unexpected key id: %v", parsed.Header["kid"])
	}
}

func TestProviderMetadata(t *testing.T) {
	t.Parallel()
	if _, _, err := providerMetadata("google"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := providerMetadata("apple"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := providerMetadata("unknown"); err == nil {
		t.Fatal("expected unsupported provider to fail")
	}
}

func TestOIDCVerifierWithLocalJWKS(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const keyID = "test-key"
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA", "kid": keyID, "use": "sig", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
			}},
		})
	}))
	defer server.Close()
	issuer = server.URL

	verifier := NewOIDCVerifier(config.Config{GoogleClientIDs: []string{"test-client"}})
	verifier.providers["google"] = oidcProviderMetadata{issuer: issuer, jwksURL: server.URL}
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"iss": issuer, "sub": "provider-user", "aud": "test-client",
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"email": "cube@example.test", "email_verified": true, "nonce": "test-nonce",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = keyID
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := verifier.Verify(context.Background(), "google", FederatedInput{
		IDToken: raw, ClientID: "test-client", Nonce: "test-nonce",
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "provider-user" || identity.Email != "cube@example.test" || !identity.EmailVerified {
		t.Fatalf("unexpected identity: %+v", identity)
	}
	if _, err := verifier.Verify(context.Background(), "google", FederatedInput{
		IDToken: raw, ClientID: "test-client", Nonce: "wrong-nonce",
	}); err == nil {
		t.Fatal("expected nonce mismatch to fail")
	}
}
