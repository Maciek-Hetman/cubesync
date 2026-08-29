package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Maciek-Hetman/cubing-sync-backend/internal/config"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
)

// countingRoundTripper counts HTTP requests to verify JWKS caching behavior.
type countingRoundTripper struct {
	handler      http.Handler
	requestCount int64
}

func (rt *countingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	atomic.AddInt64(&rt.requestCount, 1)
	inMem := &inMemoryRoundTripper{handler: rt.handler}
	return inMem.RoundTrip(req)
}

func createTestJWKSServer(t *testing.T, key *rsa.PrivateKey, keyID string) (http.Handler, string, string) {
	t.Helper()
	const issuer = "https://accounts.google.test"
	const jwksURL = "https://accounts.google.test/jwks"

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA", "kid": keyID, "use": "sig", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
			}},
		})
	})
	return handler, issuer, jwksURL
}

func generateSignedJWT(t *testing.T, key *rsa.PrivateKey, keyID, issuer, clientID, nonce string, expOffset time.Duration, emailVerified any, email, sub string) string {
	t.Helper()
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"iss":   issuer,
		"sub":   sub,
		"aud":   clientID,
		"iat":   now.Unix(),
		"exp":   now.Add(expOffset).Unix(),
		"email": email,
		"nonce": nonce,
	}
	if emailVerified != nil {
		claims["email_verified"] = emailVerified
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = keyID
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("failed to sign jwt: %v", err)
	}
	return raw
}

// TestOIDCVerifierReusesCachedKeySets verifies that OIDCVerifier caches remote key sets
// and does not make redundant network requests for multiple token verifications.
func TestOIDCVerifierReusesCachedKeySets(t *testing.T) {
	t.Parallel()

	trustedKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const keyID = "cached-key-id"

	handler, issuer, jwksURL := createTestJWKSServer(t, trustedKey, keyID)
	counter := &countingRoundTripper{handler: handler}
	httpClient := &http.Client{Transport: counter}
	ctx := oidc.ClientContext(context.Background(), httpClient)

	cfg := config.Config{GoogleClientIDs: []string{"client-app-1", "client-app-2"}}
	verifier := NewOIDCVerifier(cfg)
	verifier.providers["google"] = oidcProviderMetadata{issuer: issuer, jwksURL: jwksURL}
	verifier.keySets["google"] = oidc.NewRemoteKeySet(ctx, jwksURL)

	const numTokens = 15
	for i := 0; i < numTokens; i++ {
		token := generateSignedJWT(t, trustedKey, keyID, issuer, "client-app-1", "test-nonce", 5*time.Minute, true, "user@test.org", "sub-123")
		identity, err := verifier.Verify(ctx, "google", FederatedInput{
			IDToken:  token,
			ClientID: "client-app-1",
			Nonce:    "test-nonce",
		})
		if err != nil {
			t.Fatalf("verification %d failed: %v", i, err)
		}
		if identity.Email != "user@test.org" {
			t.Fatalf("verification %d unexpected email: %s", i, identity.Email)
		}
	}

	// Verify that remote JWKS endpoint was requested only once (keyset caching confirmed)
	reqCount := atomic.LoadInt64(&counter.requestCount)
	if reqCount != 1 {
		t.Fatalf("expected exactly 1 JWKS HTTP request due to caching, got %d", reqCount)
	}
}

// TestOIDCVerifierRejectsAdversarialTokens tests rejection of malformed, expired,
// untrusted-key, mismatched nonce/audience, and fraudulent tokens.
func TestOIDCVerifierRejectsAdversarialTokens(t *testing.T) {
	t.Parallel()

	trustedKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	untrustedKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const keyID = "trusted-key-id"

	handler, issuer, jwksURL := createTestJWKSServer(t, trustedKey, keyID)
	httpClient := &http.Client{Transport: &inMemoryRoundTripper{handler: handler}}
	ctx := oidc.ClientContext(context.Background(), httpClient)

	cfg := config.Config{GoogleClientIDs: []string{"valid-client-id"}}
	verifier := NewOIDCVerifier(cfg)
	verifier.providers["google"] = oidcProviderMetadata{issuer: issuer, jwksURL: jwksURL}
	verifier.keySets["google"] = oidc.NewRemoteKeySet(ctx, jwksURL)

	testCases := []struct {
		name          string
		provider      string
		input         FederatedInput
		expectedError string
	}{
		{
			name:     "Empty nonce in input",
			provider: "google",
			input: FederatedInput{
				IDToken:  "some-token",
				ClientID: "valid-client-id",
				Nonce:    "",
			},
			expectedError: "nonce is required",
		},
		{
			name:     "Disallowed client_id",
			provider: "google",
			input: FederatedInput{
				IDToken:  "some-token",
				ClientID: "unauthorized-client-id",
				Nonce:    "nonce-123",
			},
			expectedError: "client_id is not allowed",
		},
		{
			name:     "Unsupported provider",
			provider: "facebook",
			input: FederatedInput{
				IDToken:  "some-token",
				ClientID: "valid-client-id",
				Nonce:    "nonce-123",
			},
			expectedError: "client_id is not allowed",
		},
		{
			name:     "Malformed JWT string (not base64)",
			provider: "google",
			input: FederatedInput{
				IDToken:  "this.is-not.a-valid-jwt-token!",
				ClientID: "valid-client-id",
				Nonce:    "nonce-123",
			},
			expectedError: "verify id token",
		},
		{
			name:     "Completely empty id_token and empty code",
			provider: "google",
			input: FederatedInput{
				IDToken:  "",
				ClientID: "valid-client-id",
				Nonce:    "nonce-123",
			},
			expectedError: "code, redirect_uri, and code_verifier are required",
		},
		{
			name:     "Expired token",
			provider: "google",
			input: FederatedInput{
				IDToken:  generateSignedJWT(t, trustedKey, keyID, issuer, "valid-client-id", "nonce-123", -10*time.Minute, true, "user@test.com", "sub-1"),
				ClientID: "valid-client-id",
				Nonce:    "nonce-123",
			},
			expectedError: "verify id token",
		},
		{
			name:     "Token signed by untrusted RSA key",
			provider: "google",
			input: FederatedInput{
				IDToken:  generateSignedJWT(t, untrustedKey, "untrusted-kid", issuer, "valid-client-id", "nonce-123", 5*time.Minute, true, "user@test.com", "sub-1"),
				ClientID: "valid-client-id",
				Nonce:    "nonce-123",
			},
			expectedError: "verify id token",
		},
		{
			name:     "Token signed by untrusted key with trusted kid in header",
			provider: "google",
			input: FederatedInput{
				IDToken:  generateSignedJWT(t, untrustedKey, keyID, issuer, "valid-client-id", "nonce-123", 5*time.Minute, true, "user@test.com", "sub-1"),
				ClientID: "valid-client-id",
				Nonce:    "nonce-123",
			},
			expectedError: "verify id token",
		},
		{
			name:     "Token with wrong issuer",
			provider: "google",
			input: FederatedInput{
				IDToken:  generateSignedJWT(t, trustedKey, keyID, "https://evil-attacker.com", "valid-client-id", "nonce-123", 5*time.Minute, true, "user@test.com", "sub-1"),
				ClientID: "valid-client-id",
				Nonce:    "nonce-123",
			},
			expectedError: "verify id token",
		},
		{
			name:     "Token with audience mismatch",
			provider: "google",
			input: FederatedInput{
				IDToken:  generateSignedJWT(t, trustedKey, keyID, issuer, "other-client-id", "nonce-123", 5*time.Minute, true, "user@test.com", "sub-1"),
				ClientID: "valid-client-id",
				Nonce:    "nonce-123",
			},
			expectedError: "identity token audience or nonce is invalid",
		},
		{
			name:     "Token with nonce mismatch",
			provider: "google",
			input: FederatedInput{
				IDToken:  generateSignedJWT(t, trustedKey, keyID, issuer, "valid-client-id", "different-nonce", 5*time.Minute, true, "user@test.com", "sub-1"),
				ClientID: "valid-client-id",
				Nonce:    "nonce-123",
			},
			expectedError: "identity token audience or nonce is invalid",
		},
		{
			name:     "Token with unverified email (boolean false)",
			provider: "google",
			input: FederatedInput{
				IDToken:  generateSignedJWT(t, trustedKey, keyID, issuer, "valid-client-id", "nonce-123", 5*time.Minute, false, "user@test.com", "sub-1"),
				ClientID: "valid-client-id",
				Nonce:    "nonce-123",
			},
			expectedError: "provider must supply a verified email",
		},
		{
			name:     "Token with unverified email (string false)",
			provider: "google",
			input: FederatedInput{
				IDToken:  generateSignedJWT(t, trustedKey, keyID, issuer, "valid-client-id", "nonce-123", 5*time.Minute, "false", "user@test.com", "sub-1"),
				ClientID: "valid-client-id",
				Nonce:    "nonce-123",
			},
			expectedError: "provider must supply a verified email",
		},
		{
			name:     "Token with empty subject",
			provider: "google",
			input: FederatedInput{
				IDToken:  generateSignedJWT(t, trustedKey, keyID, issuer, "valid-client-id", "nonce-123", 5*time.Minute, true, "user@test.com", ""),
				ClientID: "valid-client-id",
				Nonce:    "nonce-123",
			},
			expectedError: "provider must supply a verified email",
		},
		{
			name:     "Token with empty email",
			provider: "google",
			input: FederatedInput{
				IDToken:  generateSignedJWT(t, trustedKey, keyID, issuer, "valid-client-id", "nonce-123", 5*time.Minute, true, "", "sub-1"),
				ClientID: "valid-client-id",
				Nonce:    "nonce-123",
			},
			expectedError: "provider must supply a verified email",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := verifier.Verify(ctx, tc.provider, tc.input)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.expectedError)
			}
			if !strings.Contains(err.Error(), tc.expectedError) {
				t.Fatalf("got error %q, want error containing %q", err.Error(), tc.expectedError)
			}
		})
	}
}

// TestOIDCVerifierECDSAAlgorithmValidation tests token verification signed with ECDSA when only RSA key is in JWKS.
func TestOIDCVerifierECDSAAlgorithmValidation(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const keyID = "rsa-only-jwks-key"

	handler, issuer, jwksURL := createTestJWKSServer(t, rsaKey, keyID)
	httpClient := &http.Client{Transport: &inMemoryRoundTripper{handler: handler}}
	ctx := oidc.ClientContext(context.Background(), httpClient)

	cfg := config.Config{GoogleClientIDs: []string{"valid-client-id"}}
	verifier := NewOIDCVerifier(cfg)
	verifier.providers["google"] = oidcProviderMetadata{issuer: issuer, jwksURL: jwksURL}
	verifier.keySets["google"] = oidc.NewRemoteKeySet(ctx, jwksURL)

	// Sign a token with ES256 using an EC key
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	claims := jwt.MapClaims{
		"iss":            issuer,
		"sub":            "user-1",
		"aud":            "valid-client-id",
		"iat":            time.Now().Unix(),
		"exp":            time.Now().Add(5 * time.Minute).Unix(),
		"email":          "test@example.com",
		"email_verified": true,
		"nonce":          "nonce-123",
	}
	ecToken := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	ecToken.Header["kid"] = keyID
	rawToken, err := ecToken.SignedString(ecKey)
	if err != nil {
		t.Fatal(err)
	}

	_, err = verifier.Verify(ctx, "google", FederatedInput{
		IDToken:  rawToken,
		ClientID: "valid-client-id",
		Nonce:    "nonce-123",
	})
	if err == nil {
		t.Fatal("expected signature verification failure for ES256 token against RSA JWKS, got nil")
	}
	if !strings.Contains(err.Error(), "verify id token") {
		t.Fatalf("unexpected error message: %v", err)
	}
}
