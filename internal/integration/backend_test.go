//go:build integration

package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Maciek-Hetman/cubing-sync-backend/db/migrations"
	"github.com/Maciek-Hetman/cubing-sync-backend/internal/auth"
	"github.com/Maciek-Hetman/cubing-sync-backend/internal/config"
	"github.com/Maciek-Hetman/cubing-sync-backend/internal/httpapi"
	syncservice "github.com/Maciek-Hetman/cubing-sync-backend/internal/sync"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestBackendIntegration(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	runMigrations(t, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "TRUNCATE users RESTART IDENTITY CASCADE"); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		DatabaseURL: databaseURL, PublicURL: "https://sync.example.test", ClientURL: "cubetimer://auth",
		JWTSecret:      []byte("integration-secret-that-is-at-least-32-bytes"),
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 30 * 24 * time.Hour,
		MaxSyncMutations: 100, MaxSyncChanges: 100,
	}
	mail := &captureMailer{}
	federated := &fakeFederatedVerifier{identities: map[string]auth.FederatedIdentity{
		"new-social": {
			Provider: "google", Subject: "google-new-user", Email: "social@example.test", EmailVerified: true,
		},
		"link-existing": {
			Provider: "google", Subject: "google-existing-user", Email: "cube@example.test", EmailVerified: true,
		},
	}}
	authService := auth.NewService(cfg, pool, mail, federated)

	t.Run("authentication lifecycle", func(t *testing.T) {
		if err := authService.Register(ctx, "cube@example.test", "correct horse battery staple"); err != nil {
			t.Fatal(err)
		}
		if _, err := authService.Login(ctx, "cube@example.test", "correct horse battery staple"); authCode(err) != "email_not_verified" {
			t.Fatalf("expected email_not_verified, got %v", err)
		}
		session, err := authService.VerifyEmail(ctx, mail.verificationToken())
		if err != nil {
			t.Fatal(err)
		}
		if !session.User.EmailVerified {
			t.Fatal("verified session does not contain a verified user")
		}
		if _, err := authService.Login(ctx, "cube@example.test", "wrong password"); authCode(err) != "invalid_credentials" {
			t.Fatalf("expected invalid_credentials, got %v", err)
		}
		loginSession, err := authService.Login(ctx, "cube@example.test", "correct horse battery staple")
		if err != nil {
			t.Fatal(err)
		}
		rotated, err := authService.Refresh(ctx, loginSession.RefreshToken)
		if err != nil {
			t.Fatal(err)
		}
		if rotated.RefreshToken == loginSession.RefreshToken {
			t.Fatal("refresh token was not rotated")
		}
		if _, err := authService.Refresh(ctx, loginSession.RefreshToken); authCode(err) != "refresh_token_reused" {
			t.Fatalf("expected refresh_token_reused, got %v", err)
		}
		if _, err := authService.Refresh(ctx, rotated.RefreshToken); authCode(err) != "invalid_refresh_token" && authCode(err) != "refresh_token_reused" {
			t.Fatalf("expected revoked token family, got %v", err)
		}
		if err := authService.ForgotPassword(ctx, "cube@example.test"); err != nil {
			t.Fatal(err)
		}
		resetSession, err := authService.ResetPassword(ctx, mail.resetToken(), "an even better password")
		if err != nil {
			t.Fatal(err)
		}
		if resetSession.User.ID != session.User.ID {
			t.Fatal("password reset changed user identity")
		}
		if _, err := authService.Login(ctx, "cube@example.test", "an even better password"); err != nil {
			t.Fatal(err)
		}

		_, err = authService.FederatedLogin(ctx, "google", auth.FederatedInput{IDToken: "link-existing"})
		if authCode(err) != "account_link_required" {
			t.Fatalf("expected account_link_required, got %v", err)
		}
		if err := authService.LinkFederated(ctx, session.User.ID, "google", auth.FederatedInput{IDToken: "link-existing"}); err != nil {
			t.Fatal(err)
		}
		linked, err := authService.FederatedLogin(ctx, "google", auth.FederatedInput{IDToken: "link-existing"})
		if err != nil || linked.User.ID != session.User.ID {
			t.Fatalf("linked identity login failed: %v", err)
		}
		social, err := authService.FederatedLogin(ctx, "google", auth.FederatedInput{IDToken: "new-social"})
		if err != nil {
			t.Fatal(err)
		}
		if social.User.Email != "social@example.test" || !social.User.EmailVerified {
			t.Fatalf("unexpected social user: %+v", social.User)
		}
	})

	user, err := authService.User(ctx, userIDByEmail(t, pool, "cube@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("two-device synchronization and conflict", func(t *testing.T) {
		testSynchronization(t, ctx, pool, user.ID)
	})
	t.Run("HTTP authentication boundary", func(t *testing.T) {
		testHTTPBoundary(t, ctx, cfg, pool, authService)
	})
}

func testSynchronization(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) {
	service := syncservice.NewService(pool, 100, 100)
	deviceA := syncservice.Device{ID: uuid.New(), Name: "Android", Platform: "android"}
	deviceB := syncservice.Device{ID: uuid.New(), Name: "Mac", Platform: "macos"}
	sessionID := uuid.New()
	solveID := uuid.New()
	startedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	sessionData := mustJSON(t, syncservice.Session{
		ID: sessionID, Name: "Lunch practice", Event: "3x3", Kind: "manual", StartedAt: startedAt,
	})
	solveData := mustJSON(t, syncservice.Solve{
		ID: solveID, SessionID: &sessionID, DurationMS: 12_345, Penalty: "none",
		SolvedAt: startedAt.Add(time.Second), Scramble: "R U R' U'", Event: "3x3",
	})
	initial, err := service.Sync(ctx, userID, syncservice.Request{
		Device: deviceA,
		Mutations: []syncservice.Mutation{
			{ID: uuid.New(), Entity: "session", EntityID: sessionID, Operation: "upsert", Data: sessionData},
			{ID: uuid.New(), Entity: "solve", EntityID: solveID, Operation: "upsert", Data: solveData},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Outcomes) != 2 || initial.Outcomes[0].Status != "accepted" || len(initial.Changes) != 2 {
		t.Fatalf("unexpected initial sync: %+v", initial)
	}

	pulled, err := service.Sync(ctx, userID, syncservice.Request{Device: deviceB})
	if err != nil {
		t.Fatal(err)
	}
	if len(pulled.Changes) != 2 {
		t.Fatalf("second device received %d changes, want 2", len(pulled.Changes))
	}

	updateA := mustJSON(t, syncservice.Solve{
		ID: solveID, SessionID: &sessionID, DurationMS: 12_345, Penalty: "plus_two",
		SolvedAt: startedAt.Add(time.Second), Scramble: "R U R' U'", Event: "3x3",
	})
	mutationID := uuid.New()
	updated, err := service.Sync(ctx, userID, syncservice.Request{
		Cursor: pulled.NextCursor, Device: deviceA,
		Mutations: []syncservice.Mutation{{
			ID: mutationID, Entity: "solve", EntityID: solveID, Operation: "upsert", BaseVersion: 1, Data: updateA,
		}},
	})
	if err != nil || updated.Outcomes[0].Version != 2 {
		t.Fatalf("update failed: response=%+v error=%v", updated, err)
	}
	retried, err := service.Sync(ctx, userID, syncservice.Request{
		Cursor: updated.NextCursor, Device: deviceA,
		Mutations: []syncservice.Mutation{{
			ID: mutationID, Entity: "solve", EntityID: solveID, Operation: "upsert", BaseVersion: 1, Data: updateA,
		}},
	})
	if err != nil || retried.Outcomes[0].Status != "accepted" || len(retried.Changes) != 0 {
		t.Fatalf("idempotent retry failed: response=%+v error=%v", retried, err)
	}

	updateB := mustJSON(t, syncservice.Solve{
		ID: solveID, SessionID: &sessionID, DurationMS: 12_345, Penalty: "dnf",
		SolvedAt: startedAt.Add(time.Second), Scramble: "R U R' U'", Event: "3x3",
	})
	conflicted, err := service.Sync(ctx, userID, syncservice.Request{
		Cursor: pulled.NextCursor, Device: deviceB,
		Mutations: []syncservice.Mutation{{
			ID: uuid.New(), Entity: "solve", EntityID: solveID, Operation: "upsert", BaseVersion: 1, Data: updateB,
		}},
	})
	if err != nil || conflicted.Outcomes[0].Status != "conflict" || conflicted.Outcomes[0].Version != 2 {
		t.Fatalf("expected version conflict: response=%+v error=%v", conflicted, err)
	}

	deleted, err := service.Sync(ctx, userID, syncservice.Request{
		Cursor: updated.NextCursor, Device: deviceA,
		Mutations: []syncservice.Mutation{{
			ID: uuid.New(), Entity: "solve", EntityID: solveID, Operation: "delete", BaseVersion: 2,
		}},
	})
	if err != nil || deleted.Outcomes[0].Version != 3 || len(deleted.Changes) != 1 || deleted.Changes[0].Operation != "delete" {
		t.Fatalf("delete tombstone failed: response=%+v error=%v", deleted, err)
	}

	otherUser := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO users (id, email, email_verified_at) VALUES ($1, $2, now())", otherUser, "other@example.test"); err != nil {
		t.Fatal(err)
	}
	isolated, err := service.Sync(ctx, otherUser, syncservice.Request{Device: syncservice.Device{ID: uuid.New()}})
	if err != nil || len(isolated.Changes) != 0 {
		t.Fatalf("cross-user change leak: response=%+v error=%v", isolated, err)
	}
}

func testHTTPBoundary(
	t *testing.T,
	ctx context.Context,
	cfg config.Config,
	pool *pgxpool.Pool,
	authService *auth.Service,
) {
	server := httptest.NewServer(httpapi.NewRouter(cfg, pool, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()

	response, err := http.Get(server.URL + "/v1/me")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated profile returned %d", response.StatusCode)
	}

	session, err := authService.Login(ctx, "cube@example.test", "an even better password")
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+session.AccessToken)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated profile returned %d", response.StatusCode)
	}

	body := mustJSON(t, syncservice.Request{
		Device: syncservice.Device{ID: uuid.New(), Name: "HTTP test", Platform: "test"},
	})
	request, err = http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/sync", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+session.AccessToken)
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated sync returned %d", response.StatusCode)
	}
}

func runMigrations(t *testing.T, databaseURL string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	goose.SetBaseFS(migrations.Files)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, "."); err != nil {
		t.Fatal(err)
	}
}

func userIDByEmail(t *testing.T, pool *pgxpool.Pool, email string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), "SELECT id FROM users WHERE email = $1", email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func authCode(err error) string {
	var authErr auth.Error
	if errors.As(err, &authErr) {
		return authErr.Code
	}
	return ""
}

type captureMailer struct {
	mu           sync.Mutex
	verification string
	reset        string
}

func (m *captureMailer) SendVerification(_ context.Context, _, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.verification = token
	return nil
}

func (m *captureMailer) SendPasswordReset(_ context.Context, _, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reset = token
	return nil
}

func (m *captureMailer) verificationToken() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.verification
}

func (m *captureMailer) resetToken() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reset
}

type fakeFederatedVerifier struct {
	identities map[string]auth.FederatedIdentity
}

func (f *fakeFederatedVerifier) Verify(_ context.Context, provider string, input auth.FederatedInput) (auth.FederatedIdentity, error) {
	identity, ok := f.identities[input.IDToken]
	if !ok || identity.Provider != provider {
		return auth.FederatedIdentity{}, auth.Error{Code: "invalid_social_token", Message: "invalid identity"}
	}
	return identity, nil
}
