package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestAccessTokenRoundTrip(t *testing.T) {
	t.Parallel()
	manager := NewTokenManager([]byte("a-secret-that-is-long-enough-for-tests"), "https://sync.example.test", 15*time.Minute)
	userID := uuid.New()
	raw, _, err := manager.IssueAccessToken(userID, true, RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	parsedID, claims, err := manager.ParseAccessToken(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsedID != userID || !claims.EmailVerified || claims.UserRole != RoleAdmin {
		t.Fatalf("unexpected claims: id=%s verified=%v role=%s", parsedID, claims.EmailVerified, claims.UserRole)
	}
}

func TestAccessTokenDefaultsEmptyRoleToUser(t *testing.T) {
	t.Parallel()
	manager := NewTokenManager([]byte("a-secret-that-is-long-enough-for-tests"), "https://sync.example.test", 15*time.Minute)
	raw, _, err := manager.IssueAccessToken(uuid.New(), true, "")
	if err != nil {
		t.Fatal(err)
	}
	_, claims, err := manager.ParseAccessToken(raw)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserRole != RoleUser {
		t.Fatalf("expected default role %q, got %q", RoleUser, claims.UserRole)
	}
}

func TestAccessTokenRejectsWrongSecret(t *testing.T) {
	t.Parallel()
	issuer := "https://sync.example.test"
	first := NewTokenManager([]byte("first-secret-that-is-long-enough"), issuer, 15*time.Minute)
	second := NewTokenManager([]byte("second-secret-that-is-long-enough"), issuer, 15*time.Minute)
	raw, _, err := first.IssueAccessToken(uuid.New(), true, RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := second.ParseAccessToken(raw); err == nil {
		t.Fatal("expected token signed with another key to fail")
	}
}

func TestOpaqueTokensAreRandomAndHashStable(t *testing.T) {
	t.Parallel()
	first, firstHash, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("random tokens unexpectedly match")
	}
	if string(firstHash) != string(tokenHash(first)) {
		t.Fatal("token hash is not stable")
	}
}

func TestAccessTokenPreservesExpiredRootError(t *testing.T) {
	t.Parallel()
	manager := NewTokenManager([]byte("a-secret-that-is-long-enough-for-tests"), "https://sync.example.test", -1*time.Minute)
	raw, _, err := manager.IssueAccessToken(uuid.New(), true, RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = manager.ParseAccessToken(raw)
	if err == nil {
		t.Fatal("expected expired token to fail")
	}
	if !errors.Is(err, jwt.ErrTokenExpired) {
		t.Fatalf("expected wrapped jwt.ErrTokenExpired, got %v", err)
	}
}
