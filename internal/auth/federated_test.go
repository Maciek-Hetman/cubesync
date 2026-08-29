package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Maciek-Hetman/cubing-sync-backend/internal/config"
	"github.com/coreos/go-oidc/v3/oidc"
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

type inMemoryRoundTripper struct {
	handler http.Handler
}

func (rt *inMemoryRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	rt.handler.ServeHTTP(rec, req)
	return rec.Result(), nil
}

func TestOIDCVerifierWithLocalJWKS(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const keyID = "test-key"
	const issuer = "https://accounts.google.test"
	const jwksURL = "https://accounts.google.test/jwks"

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA", "kid": keyID, "use": "sig", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
			}},
		})
	})
	httpClient := &http.Client{Transport: &inMemoryRoundTripper{handler: handler}}
	ctx := oidc.ClientContext(context.Background(), httpClient)

	verifier := NewOIDCVerifier(config.Config{GoogleClientIDs: []string{"test-client"}})
	verifier.providers["google"] = oidcProviderMetadata{issuer: issuer, jwksURL: jwksURL}
	verifier.keySets["google"] = oidc.NewRemoteKeySet(ctx, jwksURL)

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
	identity, err := verifier.Verify(ctx, "google", FederatedInput{
		IDToken: raw, ClientID: "test-client", Nonce: "test-nonce",
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "provider-user" || identity.Email != "cube@example.test" || !identity.EmailVerified {
		t.Fatalf("unexpected identity: %+v", identity)
	}
	if _, err := verifier.Verify(ctx, "google", FederatedInput{
		IDToken: raw, ClientID: "test-client", Nonce: "wrong-nonce",
	}); err == nil {
		t.Fatal("expected nonce mismatch to fail")
	}
}
