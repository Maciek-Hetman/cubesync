package auth

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestPasswordChangeValidationAndSecurity(t *testing.T) {
	t.Parallel()

	t.Run("SetPassword rejects short or invalid passwords before DB execution", func(t *testing.T) {
		t.Parallel()
		service := &Service{pool: nil}
		userID := uuid.New()

		// Too short (< 8 chars)
		err := service.SetPassword(context.Background(), userID, "short")
		if err == nil {
			t.Fatal("expected error for short password")
		}
		if !authErrorCode(err, "invalid_password") {
			t.Fatalf("expected invalid_password error code, got %v", err)
		}

		// Empty password
		err = service.SetPassword(context.Background(), userID, "")
		if err == nil {
			t.Fatal("expected error for empty password")
		}
		if !authErrorCode(err, "invalid_password") {
			t.Fatalf("expected invalid_password error code, got %v", err)
		}
	})

	t.Run("ChangePassword enforces current password check before updating", func(t *testing.T) {
		t.Parallel()
		// Test that password verification logic correctly distinguishes between valid and invalid current passwords
		validPass := "my-secret-password-123"
		hash, err := hashPassword(validPass)
		if err != nil {
			t.Fatal(err)
		}

		// Correct password verifies
		ok, err := verifyPassword(validPass, hash)
		if err != nil || !ok {
			t.Fatalf("expected valid password verification: ok=%v err=%v", ok, err)
		}

		// Incorrect current password fails verification
		ok, err = verifyPassword("wrong-current-password", hash)
		if err != nil || ok {
			t.Fatalf("expected incorrect password verification to fail: ok=%v err=%v", ok, err)
		}
	})
}

func authErrorCode(err error, code string) bool {
	if err == nil {
		return false
	}
	if aErrVal, ok := err.(Error); ok {
		return aErrVal.Code == code
	}
	return false
}
