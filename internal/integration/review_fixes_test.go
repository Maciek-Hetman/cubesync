//go:build integration

package integration

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Maciek-Hetman/cubing-sync-backend/internal/admin"
	"github.com/Maciek-Hetman/cubing-sync-backend/internal/auth"
	"github.com/Maciek-Hetman/cubing-sync-backend/internal/config"
	syncservice "github.com/Maciek-Hetman/cubing-sync-backend/internal/sync"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newTestPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
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
	if _, err := pool.Exec(ctx, "TRUNCATE users, request_errors, request_stats_hourly RESTART IDENTITY CASCADE"); err != nil {
		t.Fatal(err)
	}
	return ctx, pool
}

func TestAuthAccountRecoveryAndTokenRetry(t *testing.T) {
	ctx, pool := newTestPool(t)
	cfg := config.Config{
		PublicURL: "https://sync.example.test", ClientURL: "cubetimer://auth",
		JWTSecret:      []byte("integration-secret-that-is-at-least-32-bytes"),
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 30 * 24 * time.Hour,
	}
	mail := &captureMailer{}
	federated := &fakeFederatedVerifier{identities: map[string]auth.FederatedIdentity{
		"victim":   {Provider: "google", Subject: "google-victim", Email: "victim@example.test", EmailVerified: true},
		"verified": {Provider: "google", Subject: "google-verified", Email: "verified@example.test", EmailVerified: true},
	}}
	service := auth.NewService(cfg, pool, mail, federated)
	const password = "correct horse battery staple"

	t.Run("google sign-in claims unverified pre-registration", func(t *testing.T) {
		if err := service.Register(ctx, "victim@example.test", "attacker password 123"); err != nil {
			t.Fatal(err)
		}
		registeredID := userIDByEmail(t, pool, "victim@example.test")
		session, err := service.FederatedLogin(ctx, "google", auth.FederatedInput{IDToken: "victim"})
		if err != nil {
			t.Fatal(err)
		}
		if session.User.ID != registeredID || !session.User.EmailVerified {
			t.Fatalf("unexpected claimed user: %+v", session.User)
		}
		if _, err := service.Login(ctx, "victim@example.test", "attacker password 123"); authCode(err) != "invalid_credentials" {
			t.Fatalf("attacker password still works: %v", err)
		}
		if _, err := service.VerifyEmail(ctx, mail.verificationToken()); authCode(err) != "invalid_token" {
			t.Fatalf("pre-registration verification token still valid: %v", err)
		}
		err = service.ChangePassword(ctx, registeredID, "", "a brand new password")
		if authCode(err) != "password_not_set" {
			t.Fatalf("expected password_not_set, got %v", err)
		}
	})

	t.Run("verified account still requires linking", func(t *testing.T) {
		if _, err := pool.Exec(ctx, "INSERT INTO users (id, email, email_verified_at) VALUES ($1, $2, now())", uuid.New(), "verified@example.test"); err != nil {
			t.Fatal(err)
		}
		if _, err := service.FederatedLogin(ctx, "google", auth.FederatedInput{IDToken: "verified"}); authCode(err) != "account_link_required" {
			t.Fatalf("expected account_link_required, got %v", err)
		}
	})

	t.Run("password reset verifies email", func(t *testing.T) {
		if err := service.Register(ctx, "reset@example.test", password); err != nil {
			t.Fatal(err)
		}
		if err := service.ForgotPassword(ctx, "reset@example.test"); err != nil {
			t.Fatal(err)
		}
		session, err := service.ResetPassword(ctx, mail.resetToken(), "another strong password")
		if err != nil {
			t.Fatal(err)
		}
		if !session.User.EmailVerified {
			t.Fatal("reset session is not verified")
		}
		if _, err := service.Login(ctx, "reset@example.test", "another strong password"); err != nil {
			t.Fatalf("login after reset: %v", err)
		}
	})

	t.Run("refresh retry within grace window", func(t *testing.T) {
		session, err := service.Login(ctx, "reset@example.test", "another strong password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Refresh(ctx, session.RefreshToken); err != nil {
			t.Fatal(err)
		}
		retried, err := service.Refresh(ctx, session.RefreshToken)
		if err != nil {
			t.Fatalf("retry inside grace window: %v", err)
		}
		if _, err := pool.Exec(ctx, "UPDATE refresh_tokens SET used_at = used_at - interval '1 minute' WHERE used_at IS NOT NULL"); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Refresh(ctx, session.RefreshToken); authCode(err) != "refresh_token_reused" {
			t.Fatalf("expected refresh_token_reused, got %v", err)
		}
		if _, err := service.Refresh(ctx, retried.RefreshToken); authCode(err) != "refresh_token_reused" {
			t.Fatalf("expected revoked family, got %v", err)
		}
	})
}

func TestSyncConcurrentRequestsAndSnapshotWatermark(t *testing.T) {
	ctx, pool := newTestPool(t)
	userID := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO users (id, email, email_verified_at) VALUES ($1, $2, now())", userID, "concurrent@example.test"); err != nil {
		t.Fatal(err)
	}
	service := syncservice.NewService(pool, 100, 100, 512*1024, 90*24*time.Hour)
	snapshots := syncservice.NewSnapshotService(pool, 512*1024)
	sessionMutation := func(mutationID, sessionID uuid.UUID) syncservice.Mutation {
		return syncservice.Mutation{
			ID: mutationID, Entity: "session", EntityID: sessionID, Operation: "upsert",
			Data: mustJSON(t, syncservice.Session{
				ID: sessionID, Name: "Race", Event: "3x3", Kind: "manual", StartedAt: time.Now().UTC(),
			}),
		}
	}
	runConcurrently := func(requests ...syncservice.Request) []syncservice.Response {
		responses := make([]syncservice.Response, len(requests))
		errs := make([]error, len(requests))
		var wg sync.WaitGroup
		for i, request := range requests {
			wg.Add(1)
			go func() {
				defer wg.Done()
				responses[i], errs[i] = service.Sync(ctx, userID, request, 2)
			}()
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("concurrent sync failed: %v", err)
			}
		}
		return responses
	}

	t.Run("snapshot keeps first-page watermark", func(t *testing.T) {
		device := syncservice.Device{ID: uuid.New(), Name: "Bootstrap", Platform: "test"}
		first, err := snapshots.Snapshot(ctx, userID, syncservice.SnapshotRequest{Device: device, Entity: "session"})
		if err != nil {
			t.Fatal(err)
		}
		if first.Cursor != 0 {
			t.Fatalf("expected empty watermark, got %d", first.Cursor)
		}
		other := syncservice.Device{ID: uuid.New(), Name: "Other", Platform: "test"}
		if _, err := service.Sync(ctx, userID, syncservice.Request{
			Device: other, Mutations: []syncservice.Mutation{sessionMutation(uuid.New(), uuid.New())},
		}, 2); err != nil {
			t.Fatal(err)
		}
		next, err := snapshots.Snapshot(ctx, userID, syncservice.SnapshotRequest{Device: device, Entity: "solve", Cursor: first.Cursor})
		if err != nil {
			t.Fatal(err)
		}
		if next.Cursor != 0 {
			t.Fatalf("watermark moved to %d mid-bootstrap", next.Cursor)
		}
	})

	t.Run("duplicate request is applied once", func(t *testing.T) {
		device := syncservice.Device{ID: uuid.New(), Name: "Retry", Platform: "test"}
		request := syncservice.Request{Device: device, Mutations: []syncservice.Mutation{sessionMutation(uuid.New(), uuid.New())}}
		responses := runConcurrently(request, request)
		for _, response := range responses {
			if outcome := response.Outcomes[0]; outcome.Status != "accepted" || outcome.Version != 1 {
				t.Fatalf("unexpected outcome: %+v", outcome)
			}
		}
		var changes int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM change_log WHERE entity_id = $1", request.Mutations[0].EntityID).Scan(&changes); err != nil {
			t.Fatal(err)
		}
		if changes != 1 {
			t.Fatalf("expected one change_log entry, got %d", changes)
		}
	})

	t.Run("two devices create the same entity", func(t *testing.T) {
		sessionID := uuid.New()
		responses := runConcurrently(
			syncservice.Request{Device: syncservice.Device{ID: uuid.New(), Platform: "a"}, Mutations: []syncservice.Mutation{sessionMutation(uuid.New(), sessionID)}},
			syncservice.Request{Device: syncservice.Device{ID: uuid.New(), Platform: "b"}, Mutations: []syncservice.Mutation{sessionMutation(uuid.New(), sessionID)}},
		)
		statuses := map[string]int{}
		for _, response := range responses {
			statuses[response.Outcomes[0].Status]++
		}
		if statuses["accepted"] != 1 || statuses["conflict"] != 1 {
			t.Fatalf("expected one accepted and one conflict, got %v", statuses)
		}
	})
}

func TestAdminTelemetryFlushAndErrorPaging(t *testing.T) {
	ctx, pool := newTestPool(t)
	recorder := admin.NewService(pool)
	for _, ms := range []int{10, 20, 30} {
		recorder.RecordRequestAsync("POST", "/v1/sync", 200, time.Duration(ms)*time.Millisecond)
	}
	for i := 0; i < 3; i++ {
		recorder.RecordErrorAsync(uuid.New(), "POST", "/v1/sync", 500, "internal_error", "boom")
	}
	recorder.RecordErrorAsync(uuid.Nil, "GET", "/v1/me", 401, "unauthorized", "no token")
	recorder.Shutdown()
	recorder.Shutdown()
	recorder.RecordRequestAsync("POST", "/v1/sync", 200, time.Millisecond)

	var count, total, maxMS int64
	if err := pool.QueryRow(ctx, `SELECT request_count, total_duration_ms, max_duration_ms
		FROM request_stats_hourly WHERE route = '/v1/sync' AND status_code = 200`).Scan(&count, &total, &maxMS); err != nil {
		t.Fatal(err)
	}
	if count != 3 || total != 60 || maxMS != 30 {
		t.Fatalf("aggregated stats = count %d total %d max %d", count, total, maxMS)
	}

	// All three errors were copied in one statement, so they share created_at.
	reader := admin.NewService(pool)
	defer reader.Shutdown()
	first, err := reader.ListErrors(ctx, time.Time{}, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Errors) != 2 || first.NextCursor == nil || first.NextCursorID == nil {
		t.Fatalf("unexpected first page: %+v", first)
	}
	second, err := reader.ListErrors(ctx, *first.NextCursor, *first.NextCursorID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Errors) != 1 {
		t.Fatalf("expected the remaining error (401s are not logged), got %d", len(second.Errors))
	}
	for _, e := range first.Errors {
		if e.ID == second.Errors[0].ID {
			t.Fatal("error repeated across pages")
		}
	}
	empty, err := reader.ListErrors(ctx, second.Errors[0].CreatedAt, second.Errors[0].ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Errors == nil || len(empty.Errors) != 0 {
		t.Fatalf("expected empty non-nil page, got %#v", empty.Errors)
	}
}
