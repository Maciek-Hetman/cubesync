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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Maciek-Hetman/cubing-sync-backend/db/migrations"
	"github.com/Maciek-Hetman/cubing-sync-backend/internal/admin"
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
	t.Run("admin usage statistics", func(t *testing.T) {
		testAdminStatistics(t, ctx, cfg, pool, authService)
	})
}

func testSynchronization(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) {
	service := syncservice.NewService(pool, 100, 100, 512*1024, 90*24*time.Hour)
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
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Outcomes) != 2 || initial.Outcomes[0].Status != "accepted" || len(initial.Changes) != 2 {
		t.Fatalf("unexpected initial sync: %+v", initial)
	}

	pulled, err := service.Sync(ctx, userID, syncservice.Request{Device: deviceB}, 1)
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
	}, 1)
	if err != nil || updated.Outcomes[0].Version != 2 {
		t.Fatalf("update failed: response=%+v error=%v", updated, err)
	}
	retried, err := service.Sync(ctx, userID, syncservice.Request{
		Cursor: updated.NextCursor, Device: deviceA,
		Mutations: []syncservice.Mutation{{
			ID: mutationID, Entity: "solve", EntityID: solveID, Operation: "upsert", BaseVersion: 1, Data: updateA,
		}},
	}, 1)
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
	}, 1)
	if err != nil || conflicted.Outcomes[0].Status != "conflict" || conflicted.Outcomes[0].Version != 2 {
		t.Fatalf("expected version conflict: response=%+v error=%v", conflicted, err)
	}

	deleted, err := service.Sync(ctx, userID, syncservice.Request{
		Cursor: updated.NextCursor, Device: deviceA,
		Mutations: []syncservice.Mutation{{
			ID: uuid.New(), Entity: "solve", EntityID: solveID, Operation: "delete", BaseVersion: 2,
		}},
	}, 1)
	if err != nil || deleted.Outcomes[0].Version != 3 || len(deleted.Changes) != 1 || deleted.Changes[0].Operation != "delete" {
		t.Fatalf("delete tombstone failed: response=%+v error=%v", deleted, err)
	}

	otherUser := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO users (id, email, email_verified_at) VALUES ($1, $2, now())", otherUser, "other@example.test"); err != nil {
		t.Fatal(err)
	}
	isolated, err := service.Sync(ctx, otherUser, syncservice.Request{Device: syncservice.Device{ID: uuid.New()}}, 1)
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

func testAdminStatistics(
	t *testing.T,
	ctx context.Context,
	cfg config.Config,
	pool *pgxpool.Pool,
	authService *auth.Service,
) {
	if _, err := pool.Exec(ctx, "TRUNCATE request_stats_hourly"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.NewRouter(cfg, pool, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()

	unauth, err := http.Get(server.URL + "/v1/admin/stats/overview")
	if err != nil {
		t.Fatal(err)
	}
	_ = unauth.Body.Close()
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated admin overview returned %d", unauth.StatusCode)
	}

	userSession, err := authService.Login(ctx, "cube@example.test", "an even better password")
	if err != nil {
		t.Fatal(err)
	}
	if userSession.User.UserRole != auth.RoleUser {
		t.Fatalf("expected default role user, got %q", userSession.User.UserRole)
	}
	forbidden := adminGET(t, ctx, server.URL+"/v1/admin/stats/overview", userSession.AccessToken)
	defer forbidden.Body.Close()
	if forbidden.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin overview returned %d", forbidden.StatusCode)
	}

	if _, err := authService.CreateAdmin(ctx, "cube@example.test", "a different long password"); authCode(err) != "email_in_use" {
		t.Fatal("expected existing email to be rejected without promotion")
	}
	if _, err := authService.CreateAdmin(ctx, "not-an-email", "operator secret password"); authCode(err) != "invalid_email" {
		t.Fatal("expected invalid email to be rejected")
	}
	if _, err := authService.CreateAdmin(ctx, "operator@example.test", "short"); authCode(err) != "invalid_password" {
		t.Fatal("expected invalid password to be rejected")
	}
	created, err := authService.CreateAdmin(ctx, "operator@example.test", "operator secret password")
	if err != nil {
		t.Fatal(err)
	}
	if created.UserRole != auth.RoleAdmin || !created.EmailVerified {
		t.Fatalf("created admin is not ready to sign in: %+v", created)
	}
	if _, err := authService.CreateAdmin(ctx, "operator@example.test", "operator secret password"); authCode(err) != "email_in_use" {
		t.Fatal("expected duplicate admin email to be rejected")
	}
	adminSession, err := authService.Login(ctx, "operator@example.test", "operator secret password")
	if err != nil {
		t.Fatal(err)
	}
	if adminSession.User.UserRole != auth.RoleAdmin || !adminSession.User.EmailVerified {
		t.Fatalf("admin login session is not privileged: %+v", adminSession.User)
	}

	badJSON, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/auth/login", bytes.NewReader([]byte(`{`)))
	if err != nil {
		t.Fatal(err)
	}
	badJSON.Header.Set("Content-Type", "application/json")
	badJSONResp, err := http.DefaultClient.Do(badJSON)
	if err != nil {
		t.Fatal(err)
	}
	_ = badJSONResp.Body.Close()
	if badJSONResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid json login returned %d", badJSONResp.StatusCode)
	}
	health, err := http.Get(server.URL + "/health/live")
	if err != nil {
		t.Fatal(err)
	}
	_ = health.Body.Close()
	meOK := adminGET(t, ctx, server.URL+"/v1/me", adminSession.AccessToken)
	defer meOK.Body.Close()
	if meOK.StatusCode != http.StatusOK {
		t.Fatalf("admin profile returned %d", meOK.StatusCode)
	}
	version, err := http.Get(server.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	_ = version.Body.Close()

	overviewResp := adminGET(t, ctx, server.URL+"/v1/admin/stats/overview", adminSession.AccessToken)
	defer overviewResp.Body.Close()
	if overviewResp.StatusCode != http.StatusOK {
		t.Fatalf("admin overview returned %d", overviewResp.StatusCode)
	}
	var overview admin.Overview
	if err := json.NewDecoder(overviewResp.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	if overview.TotalUsers < 2 || overview.VerifiedUsers < 2 || overview.TotalDevices < 1 || overview.TotalSessions < 1 || overview.TotalSolves < 1 {
		t.Fatalf("unexpected overview totals: %+v", overview)
	}
	if overview.NewUsers24h < 1 || overview.ActiveUsers24h < 1 {
		t.Fatalf("expected recent signup and device activity: %+v", overview)
	}

	invalidRange := adminGET(t, ctx, server.URL+"/v1/admin/stats/requests?interval=week", adminSession.AccessToken)
	defer invalidRange.Body.Close()
	if invalidRange.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid interval returned %d", invalidRange.StatusCode)
	}

	from := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	to := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	requestURL := server.URL + "/v1/admin/stats/requests?from=" + from + "&to=" + to + "&interval=hour"
	typesURL := server.URL + "/v1/admin/stats/request-types?from=" + from + "&to=" + to + "&interval=hour"
	errorsURL := server.URL + "/v1/admin/stats/errors?from=" + from + "&to=" + to + "&interval=hour"
	var requests admin.RequestSeries
	var typeStats admin.RequestTypeSeries
	var logs admin.ErrorLogResponse

	statsReady := func() bool {
		var total, status2xx, status4xx, typeTotal int64
		for _, point := range requests.Points {
			total += point.RequestCount
			status2xx += point.Status2xx
			status4xx += point.Status4xx
		}
		countsByType := make(map[string]int64, len(typeStats.Types))
		for _, entry := range typeStats.Types {
			countsByType[entry.Type] += entry.RequestCount
			typeTotal += entry.RequestCount
		}
		foundLogin400 := false
		for _, log := range logs.Errors {
			if log.Method == http.MethodPost && log.Route == "/v1/auth/login" && log.Status == http.StatusBadRequest {
				foundLogin400 = true
			}
		}
		return total >= 2 && status2xx >= 1 && status4xx >= 1 &&
			countsByType[admin.RequestTypeAuth] >= 1 &&
			countsByType[admin.RequestTypeAccount] >= 1 &&
			countsByType[admin.RequestTypeOther] >= 1 &&
			typeTotal == total &&
			foundLogin400
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		adminGETJSON(t, ctx, requestURL, adminSession.AccessToken, &requests)
		adminGETJSON(t, ctx, typesURL, adminSession.AccessToken, &typeStats)
		adminGETJSON(t, ctx, errorsURL, adminSession.AccessToken, &logs)
		if statsReady() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected recorded application requests: %+v, request types: %+v, errors: %+v", requests, typeStats, logs)
		}
		time.Sleep(500 * time.Millisecond)
	}

	var total, status2xx, status4xx int64
	for _, point := range requests.Points {
		total += point.RequestCount
		status2xx += point.Status2xx
		status4xx += point.Status4xx
		if point.RequestCount > 0 && point.AverageDurationMS < 0 {
			t.Fatalf("negative average duration: %+v", point)
		}
	}
	if total < 2 || status2xx < 1 || status4xx < 1 {
		t.Fatalf("expected recorded application requests: %+v", requests)
	}

	countsByType := make(map[string]int64, len(typeStats.Types))
	var typeTotal int64
	for _, entry := range typeStats.Types {
		if entry.RequestCount <= 0 {
			t.Fatalf("non-positive request count for type %q: %+v", entry.Type, typeStats)
		}
		countsByType[entry.Type] += entry.RequestCount
		typeTotal += entry.RequestCount
	}
	if countsByType[admin.RequestTypeAuth] < 1 || countsByType[admin.RequestTypeAccount] < 1 || countsByType[admin.RequestTypeOther] < 1 {
		t.Fatalf("expected auth, account and other request types: %+v", typeStats)
	}
	if typeTotal != total {
		t.Fatalf("request type total %d does not match request stats total %d", typeTotal, total)
	}

	foundLogin400 := false
	for _, log := range logs.Errors {
		if log.Method == http.MethodPost && log.Route == "/v1/auth/login" && log.Status == http.StatusBadRequest {
			foundLogin400 = true
		}
		if log.Route == "/health/live" || strings.HasPrefix(log.Route, "/v1/admin/stats") {
			t.Fatalf("internal route leaked into error stats: %+v", log)
		}
	}
	if !foundLogin400 {
		t.Fatalf("expected login 400 breakdown: %+v", logs)
	}
}

func adminGET(t *testing.T, ctx context.Context, url, token string) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func adminGETJSON(t *testing.T, ctx context.Context, url, token string, dst any) {
	t.Helper()
	response := adminGET(t, ctx, url, token)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("admin endpoint returned %d", response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(dst); err != nil {
		t.Fatal(err)
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

// TestSyncDeleteOfTombstoneIsIdempotent covers the scenario where two devices
// race to delete the same entity: device A's delete lands first (v1 -> v2),
// device B then resubmits its own already-queued delete mutation with
// base_version 2 (the version it just learned about from the tombstone
// change). Before the fix, DeleteSession/DeleteSolve's `deleted_at IS NULL`
// WHERE clause matched zero rows in that case, surfaced pgx.ErrNoRows, and
// rolled back the whole sync transaction as a 500 - permanently wedging the
// client's outbox since the mutation was never recorded as processed. It
// must instead be reported as an idempotent `accepted` at the unchanged
// version, without a second change_log entry. Resubmitting with a stale
// base_version (1) must still be reported as a conflict.
func TestSyncDeleteOfTombstoneIsIdempotent(t *testing.T) {
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

	userID := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO users (id, email, email_verified_at) VALUES ($1, $2, now())", userID, "tombstone@example.test"); err != nil {
		t.Fatal(err)
	}

	service := syncservice.NewService(pool, 100, 100, 512*1024, 90*24*time.Hour)
	device := syncservice.Device{ID: uuid.New(), Name: "Test device", Platform: "test"}
	sessionID := uuid.New()
	solveID := uuid.New()
	startedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)

	sessionData := mustJSON(t, syncservice.Session{
		ID: sessionID, Name: "Tombstone practice", Event: "3x3", Kind: "manual", StartedAt: startedAt,
	})
	solveData := mustJSON(t, syncservice.Solve{
		ID: solveID, SessionID: &sessionID, DurationMS: 9_999, Penalty: "none",
		SolvedAt: startedAt.Add(time.Second), Scramble: "R U R' U'", Event: "3x3",
	})
	created, err := service.Sync(ctx, userID, syncservice.Request{
		Device: device,
		Mutations: []syncservice.Mutation{
			{ID: uuid.New(), Entity: "session", EntityID: sessionID, Operation: "upsert", Data: sessionData},
			{ID: uuid.New(), Entity: "solve", EntityID: solveID, Operation: "upsert", Data: solveData},
		},
	}, 1)
	if err != nil {
		t.Fatalf("initial create failed: %v", err)
	}
	if len(created.Outcomes) != 2 || created.Outcomes[0].Status != "accepted" || created.Outcomes[1].Status != "accepted" {
		t.Fatalf("unexpected create outcomes: %+v", created.Outcomes)
	}

	// First delete of each entity: base_version 1 -> 2, accepted.
	firstDelete, err := service.Sync(ctx, userID, syncservice.Request{
		Cursor: created.NextCursor, Device: device,
		Mutations: []syncservice.Mutation{
			{ID: uuid.New(), Entity: "session", EntityID: sessionID, Operation: "delete", BaseVersion: 1},
			{ID: uuid.New(), Entity: "solve", EntityID: solveID, Operation: "delete", BaseVersion: 1},
		},
	}, 1)
	if err != nil {
		t.Fatalf("first delete failed: %v", err)
	}
	for _, outcome := range firstDelete.Outcomes {
		if outcome.Status != "accepted" || outcome.Version != 2 {
			t.Fatalf("expected first delete accepted at version 2, got %+v", outcome)
		}
	}

	changeLogCount := func(entityID uuid.UUID) int64 {
		t.Helper()
		var count int64
		if err := pool.QueryRow(ctx,
			"SELECT count(*) FROM change_log WHERE user_id = $1 AND entity_id = $2 AND operation = 'delete'",
			userID, entityID,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	sessionDeleteEntries := changeLogCount(sessionID)
	solveDeleteEntries := changeLogCount(solveID)
	if sessionDeleteEntries != 1 || solveDeleteEntries != 1 {
		t.Fatalf("expected exactly one delete change_log entry each, got session=%d solve=%d", sessionDeleteEntries, solveDeleteEntries)
	}

	// A new mutation id resubmitting the same delete at the now-current
	// base_version (2) must succeed idempotently: HTTP-level 200, outcome
	// accepted, version unchanged, and no additional change_log entry.
	resubmitted, err := service.Sync(ctx, userID, syncservice.Request{
		Cursor: firstDelete.NextCursor, Device: device,
		Mutations: []syncservice.Mutation{
			{ID: uuid.New(), Entity: "session", EntityID: sessionID, Operation: "delete", BaseVersion: 2},
			{ID: uuid.New(), Entity: "solve", EntityID: solveID, Operation: "delete", BaseVersion: 2},
		},
	}, 1)
	if err != nil {
		t.Fatalf("idempotent resubmitted delete returned an error instead of a clean outcome: %v", err)
	}
	for _, outcome := range resubmitted.Outcomes {
		if outcome.Status != "accepted" || outcome.Version != 2 {
			t.Fatalf("expected idempotent resubmitted delete to be accepted at version 2, got %+v", outcome)
		}
	}
	if len(resubmitted.Changes) != 0 {
		t.Fatalf("idempotent resubmitted delete must not produce new changes: %+v", resubmitted.Changes)
	}
	if got := changeLogCount(sessionID); got != sessionDeleteEntries {
		t.Fatalf("idempotent resubmitted session delete added a change_log entry: before=%d after=%d", sessionDeleteEntries, got)
	}
	if got := changeLogCount(solveID); got != solveDeleteEntries {
		t.Fatalf("idempotent resubmitted solve delete added a change_log entry: before=%d after=%d", solveDeleteEntries, got)
	}

	// A delete resubmitted with a stale base_version (1, the pre-tombstone
	// version) must still be reported as a conflict, not idempotent success.
	staleConflict, err := service.Sync(ctx, userID, syncservice.Request{
		Cursor: resubmitted.NextCursor, Device: device,
		Mutations: []syncservice.Mutation{
			{ID: uuid.New(), Entity: "session", EntityID: sessionID, Operation: "delete", BaseVersion: 1},
			{ID: uuid.New(), Entity: "solve", EntityID: solveID, Operation: "delete", BaseVersion: 1},
		},
	}, 1)
	if err != nil {
		t.Fatalf("stale base_version delete returned an error instead of a conflict: %v", err)
	}
	for _, outcome := range staleConflict.Outcomes {
		if outcome.Status != "conflict" || outcome.Version != 2 {
			t.Fatalf("expected stale base_version delete to conflict at version 2, got %+v", outcome)
		}
	}
}

// TestSyncCursorExpiryAfterRetentionPrune covers the interaction between
// RetentionService's prune job (internal/sync/retention.go) and
// Service.Sync's cursor-expiry check (checkCursorNotExpired in
// internal/sync/service.go): both must partition a user's devices into
// "active"/"inactive" using the same inactive-device window, or a device
// that has been away longer than the *real* retention window - but whose
// stale ack cursor would still fall inside a shorter, mismatched check
// window - can silently miss changes retention already pruned instead of
// being told to resync.
//
// It configures the sync Service with a short 7-day window (as an operator
// might via INACTIVE_DEVICE_WINDOW), backdates a second device's last_seen_at
// by 30 days so retention would treat it as inactive, prunes change_log down
// to the still-active device's ack cursor exactly as RetentionService.runOnce
// would, and then asserts the stale device's resumed sync is rejected with
// cursor_expired rather than silently returning an empty, "caught up" page.
func TestSyncCursorExpiryAfterRetentionPrune(t *testing.T) {
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

	userID := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO users (id, email, email_verified_at) VALUES ($1, $2, now())", userID, "cursor-expiry@example.test"); err != nil {
		t.Fatal(err)
	}

	// A short window makes the original bug reproducible: with the old
	// hardcoded 90-day check, a device inactive for 30 days would still
	// count as "active" for the check (even though retention, run with this
	// 7-day window, already treats it as inactive and prunes past its ack),
	// so its own stale ack would keep minValid low and the check would never
	// trip.
	const window = 7 * 24 * time.Hour
	service := syncservice.NewService(pool, 100, 100, 512*1024, window)

	deviceA := syncservice.Device{ID: uuid.New(), Name: "Desktop", Platform: "linux"}
	deviceB := syncservice.Device{ID: uuid.New(), Name: "Phone", Platform: "android"}
	sessionID := uuid.New()
	solveID := uuid.New()
	startedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)

	sessionData := mustJSON(t, syncservice.Session{
		ID: sessionID, Name: "Expiry practice", Event: "3x3", Kind: "manual", StartedAt: startedAt,
	})
	solveData := mustJSON(t, syncservice.Solve{
		ID: solveID, SessionID: &sessionID, DurationMS: 11_111, Penalty: "none",
		SolvedAt: startedAt.Add(time.Second), Scramble: "R U R' U'", Event: "3x3",
	})

	// Device A creates the first batch of changes.
	created, err := service.Sync(ctx, userID, syncservice.Request{
		Device: deviceA,
		Mutations: []syncservice.Mutation{
			{ID: uuid.New(), Entity: "session", EntityID: sessionID, Operation: "upsert", Data: sessionData},
			{ID: uuid.New(), Entity: "solve", EntityID: solveID, Operation: "upsert", Data: solveData},
		},
	}, 1)
	if err != nil {
		t.Fatalf("initial create failed: %v", err)
	}
	firstBatchCursor := created.NextCursor
	// Device A acks up through the batch it just wrote (ack advances to the
	// *request* cursor - see the comment above UpdateDeviceAckCursor's call
	// site in service.go - so this extra round trip is what actually records
	// the ack).
	if _, err := service.Sync(ctx, userID, syncservice.Request{Cursor: firstBatchCursor, Device: deviceA}, 1); err != nil {
		t.Fatalf("device A ack pull failed: %v", err)
	}

	// Device B pulls the same batch and acks it too - this is the cursor it
	// will (wrongly, once stale) keep trying to resume from below.
	pulledB, err := service.Sync(ctx, userID, syncservice.Request{Device: deviceB}, 1)
	if err != nil {
		t.Fatalf("device B initial pull failed: %v", err)
	}
	if pulledB.NextCursor != firstBatchCursor {
		t.Fatalf("device B did not see the full first batch: got cursor %d, want %d", pulledB.NextCursor, firstBatchCursor)
	}
	if _, err := service.Sync(ctx, userID, syncservice.Request{Cursor: firstBatchCursor, Device: deviceB}, 1); err != nil {
		t.Fatalf("device B ack pull failed: %v", err)
	}

	// Device B goes quiet for 30 days - long enough for a 7-day retention
	// window to treat it as inactive, but well inside the old hardcoded
	// 90-day check window that caused the bug.
	if _, err := pool.Exec(ctx,
		"UPDATE devices SET last_seen_at = now() - interval '30 days' WHERE user_id = $1 AND id = $2",
		userID, deviceB.ID,
	); err != nil {
		t.Fatal(err)
	}

	// Device A keeps syncing and fully catches up to a second batch of
	// changes, advancing its own ack cursor past the first batch.
	updatedSolve := mustJSON(t, syncservice.Solve{
		ID: solveID, SessionID: &sessionID, DurationMS: 22_222, Penalty: "plus_two",
		SolvedAt: startedAt.Add(time.Second), Scramble: "R U R' U'", Event: "3x3",
	})
	secondBatch, err := service.Sync(ctx, userID, syncservice.Request{
		Cursor: firstBatchCursor, Device: deviceA,
		Mutations: []syncservice.Mutation{
			{ID: uuid.New(), Entity: "solve", EntityID: solveID, Operation: "upsert", BaseVersion: 1, Data: updatedSolve},
		},
	}, 1)
	if err != nil {
		t.Fatalf("device A second batch failed: %v", err)
	}
	secondBatchCursor := secondBatch.NextCursor
	if secondBatchCursor <= firstBatchCursor {
		t.Fatalf("expected the second batch cursor (%d) to advance past the first batch (%d)", secondBatchCursor, firstBatchCursor)
	}
	if _, err := service.Sync(ctx, userID, syncservice.Request{Cursor: secondBatchCursor, Device: deviceA}, 1); err != nil {
		t.Fatalf("device A ack of second batch failed: %v", err)
	}

	// Run retention's prune inline. RetentionService.runOnce isn't exported
	// and normally only runs on its own ticker, so this issues the exact
	// queries it runs (MinValidCursorForUser then PruneChangeLog, both in
	// db/queries/sync.sql) with the same window, reproducing precisely what
	// the background job would do for this user.
	var minCursor int64
	if err := pool.QueryRow(ctx,
		"SELECT COALESCE(MIN(last_ack_cursor), 0)::bigint FROM devices WHERE user_id = $1 AND last_seen_at > $2",
		userID, time.Now().UTC().Add(-window),
	).Scan(&minCursor); err != nil {
		t.Fatal(err)
	}
	if minCursor != secondBatchCursor {
		t.Fatalf("expected retention's prune floor to be device A's caught-up cursor (%d, the only device still counted as active), got %d", secondBatchCursor, minCursor)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM change_log WHERE user_id = $1 AND change_id < $2", userID, minCursor); err != nil {
		t.Fatal(err)
	}

	// Device B reconnects with its old, now-pruned cursor. It must be told
	// to resync (409 cursor_expired at the HTTP layer - see
	// httpapi.synchronize) rather than silently receiving zero changes and
	// believing it is fully caught up.
	_, err = service.Sync(ctx, userID, syncservice.Request{Cursor: firstBatchCursor, Device: deviceB}, 1)
	var clientErr syncservice.ClientError
	if !errors.As(err, &clientErr) || clientErr.Code != "cursor_expired" {
		t.Fatalf("expected cursor_expired for device B's pruned cursor, got %v", err)
	}
}
