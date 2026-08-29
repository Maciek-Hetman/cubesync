package sync

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	storedb "github.com/Maciek-Hetman/cubing-sync-backend/internal/store/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var errSimulatedDB = errors.New("simulated pgx connection terminated")

// mockDBTX implements storedb.DBTX to inject specific errors and verify root cause preservation.
type mockDBTX struct {
	execErr     error
	queryErr    error
	queryRowErr error
}

func (m *mockDBTX) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	if m.execErr != nil {
		return pgconn.CommandTag{}, m.execErr
	}
	return pgconn.CommandTag{}, nil
}

func (m *mockDBTX) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	return nil, nil
}

func (m *mockDBTX) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	return &mockRow{err: m.queryRowErr}
}

type mockRow struct {
	err error
}

func (r *mockRow) Scan(dest ...interface{}) error {
	if r.err != nil {
		return r.err
	}
	return pgx.ErrNoRows
}

func TestSyncMutationPreservesDBRootCauses(t *testing.T) {
	t.Parallel()
	service := NewService(nil, 100, 100, 512*1024)
	userID := uuid.New()
	deviceID := uuid.New()
	sessionID := uuid.New()
	solveID := uuid.New()

	t.Run("session lock failure wraps DB root cause", func(t *testing.T) {
		t.Parallel()
		mock := &mockDBTX{queryRowErr: errSimulatedDB}
		q := storedb.New(mock)

		mutation := Mutation{
			ID:          uuid.New(),
			Entity:      "session",
			EntityID:    sessionID,
			Operation:   "delete",
			BaseVersion: 1,
		}

		_, err := service.applyMutation(context.Background(), q, userID, deviceID, mutation, 1)
		if err == nil {
			t.Fatal("expected error on DB failure")
		}
		if !errors.Is(err, errSimulatedDB) {
			t.Fatalf("expected errors.Is(err, errSimulatedDB) = true, got %v", err)
		}
	})

	t.Run("solve lock failure wraps DB root cause", func(t *testing.T) {
		t.Parallel()
		mock := &mockDBTX{queryRowErr: errSimulatedDB}
		q := storedb.New(mock)

		mutation := Mutation{
			ID:          uuid.New(),
			Entity:      "solve",
			EntityID:    solveID,
			Operation:   "delete",
			BaseVersion: 1,
		}

		_, err := service.applyMutation(context.Background(), q, userID, deviceID, mutation, 1)
		if err == nil {
			t.Fatal("expected error on DB failure")
		}
		if !errors.Is(err, errSimulatedDB) {
			t.Fatalf("expected errors.Is(err, errSimulatedDB) = true, got %v", err)
		}
	})

	t.Run("session upsert execution failure wraps root cause", func(t *testing.T) {
		t.Parallel()
		// Lock succeeds (returns nil), GetSession returns ErrNoRows (new session), InsertSession returns errSimulatedDB
		sessionData, err := json.Marshal(Session{
			ID:        sessionID,
			Name:      "Practice",
			Event:     "3x3",
			Kind:      "manual",
			StartedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}

		mock := &mockDBTX{queryRowErr: errSimulatedDB}
		q := storedb.New(mock)

		mutation := Mutation{
			ID:          uuid.New(),
			Entity:      "session",
			EntityID:    sessionID,
			Operation:   "upsert",
			BaseVersion: 0,
			Data:        sessionData,
		}

		_, err = service.applyMutation(context.Background(), q, userID, deviceID, mutation, 1)
		if err == nil {
			t.Fatal("expected error on DB insert failure")
		}
		if !errors.Is(err, errSimulatedDB) {
			t.Fatalf("expected errors.Is(err, errSimulatedDB) = true, got %v", err)
		}
	})
}
