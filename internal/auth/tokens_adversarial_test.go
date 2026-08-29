package auth

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestParseAccessTokenPreservesRootCauses(t *testing.T) {
	t.Parallel()
	secret := []byte("a-very-secure-and-long-secret-key-32b")
	issuer := "https://sync.example.test"
	manager := NewTokenManager(secret, issuer, 15*time.Minute)

	t.Run("expired token preserves jwt.ErrTokenExpired", func(t *testing.T) {
		t.Parallel()
		expiredManager := NewTokenManager(secret, issuer, -10*time.Minute)
		token, _, err := expiredManager.IssueAccessToken(uuid.New(), true, RoleUser)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = manager.ParseAccessToken(token)
		if err == nil {
			t.Fatal("expected error for expired token")
		}
		if !errors.Is(err, jwt.ErrTokenExpired) {
			t.Fatalf("expected errors.Is(err, jwt.ErrTokenExpired), got %v", err)
		}
	})

	t.Run("wrong signature preserves jwt.ErrTokenSignatureInvalid", func(t *testing.T) {
		t.Parallel()
		otherManager := NewTokenManager([]byte("different-secret-key-that-is-32-bytes"), issuer, 15*time.Minute)
		token, _, err := otherManager.IssueAccessToken(uuid.New(), true, RoleUser)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = manager.ParseAccessToken(token)
		if err == nil {
			t.Fatal("expected error for invalid signature")
		}
		if !errors.Is(err, jwt.ErrTokenSignatureInvalid) {
			t.Fatalf("expected errors.Is(err, jwt.ErrTokenSignatureInvalid), got %v", err)
		}
	})

	t.Run("malformed token preserves jwt.ErrTokenMalformed", func(t *testing.T) {
		t.Parallel()
		malformedTokens := []string{
			"not-a-valid-jwt",
			"header.payload",
			"a.b.c.d",
			"",
		}
		for _, raw := range malformedTokens {
			_, _, err := manager.ParseAccessToken(raw)
			if err == nil {
				t.Fatalf("expected error for malformed token %q", raw)
			}
			if !errors.Is(err, jwt.ErrTokenMalformed) {
				t.Fatalf("expected errors.Is(err, jwt.ErrTokenMalformed) for %q, got %v", raw, err)
			}
		}
	})

	t.Run("invalid issuer preserves jwt.ErrTokenInvalidIssuer", func(t *testing.T) {
		t.Parallel()
		wrongIssuerManager := NewTokenManager(secret, "https://wrong.issuer.test", 15*time.Minute)
		token, _, err := wrongIssuerManager.IssueAccessToken(uuid.New(), true, RoleUser)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = manager.ParseAccessToken(token)
		if err == nil {
			t.Fatal("expected error for wrong issuer")
		}
		if !errors.Is(err, jwt.ErrTokenInvalidIssuer) {
			t.Fatalf("expected errors.Is(err, jwt.ErrTokenInvalidIssuer), got %v", err)
		}
	})

	t.Run("invalid subject UUID returns descriptive wrapped error", func(t *testing.T) {
		t.Parallel()
		now := time.Now().UTC()
		claims := AccessClaims{
			EmailVerified: true,
			UserRole:      RoleUser,
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    issuer,
				Subject:   "not-a-valid-uuid-format",
				Audience:  jwt.ClaimStrings{"cubetimer-clients"},
				ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
				IssuedAt:  jwt.NewNumericDate(now),
			},
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := tok.SignedString(secret)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = manager.ParseAccessToken(signed)
		if err == nil {
			t.Fatal("expected error for invalid subject UUID")
		}
		if !strings.Contains(err.Error(), "invalid access token subject") {
			t.Fatalf("expected 'invalid access token subject' in error message, got %v", err)
		}
	})

	t.Run("tampered payload preserves signature invalid error", func(t *testing.T) {
		t.Parallel()
		token, _, err := manager.IssueAccessToken(uuid.New(), true, RoleUser)
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			t.Fatalf("unexpected jwt parts count: %d", len(parts))
		}
		// Tamper with payload
		tampered := parts[0] + ".eyJhZG1pbiI6dHJ1ZX0." + parts[2]
		_, _, err = manager.ParseAccessToken(tampered)
		if err == nil {
			t.Fatal("expected error for tampered token")
		}
		if !errors.Is(err, jwt.ErrTokenSignatureInvalid) {
			t.Fatalf("expected errors.Is(err, jwt.ErrTokenSignatureInvalid), got %v", err)
		}
	})

	t.Run("none algorithm rejected", func(t *testing.T) {
		t.Parallel()
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
		now := time.Now().UTC()
		payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"` + uuid.NewString() + `","iss":"` + issuer + `","aud":["cubetimer-clients"],"exp":` + string(rune(now.Add(time.Hour).Unix())) + `}`))
		unsignedToken := header + "." + payload + "."
		_, _, err := manager.ParseAccessToken(unsignedToken)
		if err == nil {
			t.Fatal("expected 'none' algorithm token to be rejected")
		}
	})
}
