package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Maciek-Hetman/cubing-sync-backend/internal/auth"
	"github.com/Maciek-Hetman/cubing-sync-backend/internal/config"
)

type fakeTerminal struct {
	terminal bool
	secrets  []string
}

func (t *fakeTerminal) IsTerminal(int) bool { return t.terminal }

func (t *fakeTerminal) ReadPassword(int) ([]byte, error) {
	if len(t.secrets) == 0 {
		return nil, io.EOF
	}
	secret := t.secrets[0]
	t.secrets = t.secrets[1:]
	return []byte(secret), nil
}

func TestCreateAdminRejectsArguments(t *testing.T) {
	t.Parallel()
	err := createAdmin(config.Config{}, []string{"admin@example.test"}, os.Stdin, io.Discard, io.Discard, &fakeTerminal{terminal: true})
	if err == nil || err.Error() != "unexpected arguments" {
		t.Fatalf("got %v", err)
	}
}

func TestCreateAdminRequiresTerminal(t *testing.T) {
	t.Parallel()
	err := createAdmin(config.Config{DatabaseURL: "postgres://example.invalid/test"}, nil, os.Stdin, io.Discard, io.Discard, &fakeTerminal{})
	if err == nil || err.Error() != "interactive terminal required" {
		t.Fatalf("got %v", err)
	}
}

func TestPromptAdminCredentialsRejectsMismatchAndEmptyEmail(t *testing.T) {
	t.Parallel()
	empty, err := promptWithInput(t, "\n", []string{"long-enough-password", "long-enough-password"})
	if err == nil || err.Error() != "email is required" {
		t.Fatalf("empty email: creds=%+v err=%v", empty, err)
	}
	mismatch, err := promptWithInput(t, "admin@example.test\n", []string{"long-enough-password", "different-password"})
	if err == nil || err.Error() != "passwords do not match" {
		t.Fatalf("mismatch: creds=%+v err=%v", mismatch, err)
	}
	creds, err := promptWithInput(t, "admin@example.test\n", []string{"long-enough-password", "long-enough-password"})
	if err != nil {
		t.Fatal(err)
	}
	if creds.email != "admin@example.test" || creds.password != "long-enough-password" {
		t.Fatalf("unexpected credentials: %+v", creds)
	}
}

func TestPublicCreateAdminErrorHidesInternalDetails(t *testing.T) {
	t.Parallel()
	if got := publicCreateAdminError(auth.Error{Code: "email_in_use", Message: "an account already exists for this email"}); got != "an account already exists for this email" {
		t.Fatalf("got %q", got)
	}
	if got := publicCreateAdminError(errors.New("column user_role does not exist")); got != "database schema is not up to date; run migrate first" {
		t.Fatalf("got %q", got)
	}
	if got := publicCreateAdminError(errors.New("connection refused")); !strings.Contains(got, "could not be created") {
		t.Fatalf("got %q", got)
	}
}

func promptWithInput(t *testing.T, emailLine string, secrets []string) (adminCredentials, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	if _, err := io.WriteString(writer, emailLine); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	return promptAdminCredentials(reader, &bytes.Buffer{}, &fakeTerminal{terminal: true, secrets: secrets})
}
