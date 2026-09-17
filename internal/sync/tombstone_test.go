package sync

import (
	"context"
	"errors"
	"testing"
	"time"

	storedb "github.com/Maciek-Hetman/cubing-sync-backend/internal/store/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// tombstoneMockDBTX implements storedb.DBTX. It returns a single canned
// "for update" row (already tombstoned) for the first QueryRow call and then
// tracks how many further Exec/QueryRow calls are made, so tests can assert
// that an idempotent delete short-circuits before touching DeleteX or
// AppendChange.
type tombstoneMockDBTX struct {
	sessionRow *storedb.CubeSession
	solveRow   *storedb.Solf

	execCalls     int
	queryRowCalls int
}

func (m *tombstoneMockDBTX) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	m.execCalls++
	return pgconn.CommandTag{}, nil
}

func (m *tombstoneMockDBTX) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	return nil, nil
}

func (m *tombstoneMockDBTX) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	m.queryRowCalls++
	if m.queryRowCalls == 1 {
		if m.sessionRow != nil {
			return &fakeSessionRow{row: *m.sessionRow}
		}
		if m.solveRow != nil {
			return &fakeSolveRow{row: *m.solveRow}
		}
	}
	// Any call beyond the first GetXForUpdate (e.g. DeleteSession/DeleteSolve
	// or AppendChange) means the idempotent short-circuit was NOT taken.
	return &fakeErrRow{err: errUnexpectedQuery}
}

var errUnexpectedQuery = errors.New("tombstoneMockDBTX: unexpected query beyond the initial GetXForUpdate")

type fakeErrRow struct{ err error }

func (r *fakeErrRow) Scan(dest ...interface{}) error { return r.err }

type fakeSessionRow struct{ row storedb.CubeSession }

func (r *fakeSessionRow) Scan(dest ...interface{}) error {
	*dest[0].(*uuid.UUID) = r.row.ID
	*dest[1].(*uuid.UUID) = r.row.UserID
	*dest[2].(*string) = r.row.Name
	*dest[3].(*string) = r.row.Event
	*dest[4].(*string) = r.row.Kind
	*dest[5].(*time.Time) = r.row.StartedAt
	*dest[6].(**time.Time) = r.row.EndedAt
	*dest[7].(*bool) = r.row.Archived
	*dest[8].(*int64) = r.row.Version
	*dest[9].(*time.Time) = r.row.UpdatedAt
	*dest[10].(**time.Time) = r.row.DeletedAt
	return nil
}

type fakeSolveRow struct{ row storedb.Solf }

func (r *fakeSolveRow) Scan(dest ...interface{}) error {
	*dest[0].(*uuid.UUID) = r.row.ID
	*dest[1].(*uuid.UUID) = r.row.UserID
	*dest[2].(*uuid.NullUUID) = r.row.SessionID
	*dest[3].(*int64) = r.row.DurationMs
	*dest[4].(*string) = r.row.Penalty
	*dest[5].(*time.Time) = r.row.SolvedAt
	*dest[6].(*string) = r.row.Scramble
	*dest[7].(*string) = r.row.Event
	*dest[8].(*int64) = r.row.Version
	*dest[9].(*time.Time) = r.row.UpdatedAt
	*dest[10].(**time.Time) = r.row.DeletedAt
	return nil
}

func TestApplySessionDeleteOfTombstoneIsIdempotent(t *testing.T) {
	t.Parallel()
	service := NewService(nil, 100, 100, 512*1024, 90*24*time.Hour)
	userID := uuid.New()
	sessionID := uuid.New()
	deletedAt := time.Now().UTC()
	mock := &tombstoneMockDBTX{sessionRow: &storedb.CubeSession{
		ID: sessionID, UserID: userID, Name: "n", Event: "3x3", Kind: "manual",
		StartedAt: deletedAt.Add(-time.Hour), Version: 2, UpdatedAt: deletedAt, DeletedAt: &deletedAt,
	}}
	q := storedb.New(mock)

	mutation := Mutation{ID: uuid.New(), Entity: "session", EntityID: sessionID, Operation: "delete", BaseVersion: 2}
	outcome, err := service.applySession(context.Background(), q, userID, mutation, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Status != "accepted" || outcome.Version != 2 {
		t.Fatalf("expected idempotent accepted outcome at version 2, got %+v", outcome)
	}
	if mock.queryRowCalls != 1 {
		t.Fatalf("expected only the GetSessionForUpdate query row call, got %d query row calls", mock.queryRowCalls)
	}
	if mock.execCalls != 0 {
		t.Fatalf("expected no exec calls (advisory lock is in Sync, not applySession), got %d exec calls", mock.execCalls)
	}
}

func TestApplySolveDeleteOfTombstoneIsIdempotent(t *testing.T) {
	t.Parallel()
	service := NewService(nil, 100, 100, 512*1024, 90*24*time.Hour)
	userID := uuid.New()
	solveID := uuid.New()
	deletedAt := time.Now().UTC()
	mock := &tombstoneMockDBTX{solveRow: &storedb.Solf{
		ID: solveID, UserID: userID, DurationMs: 1000, Penalty: "none",
		SolvedAt: deletedAt.Add(-time.Hour), Scramble: "R U R' U'", Event: "3x3",
		Version: 2, UpdatedAt: deletedAt, DeletedAt: &deletedAt,
	}}
	q := storedb.New(mock)

	mutation := Mutation{ID: uuid.New(), Entity: "solve", EntityID: solveID, Operation: "delete", BaseVersion: 2}
	outcome, err := service.applySolve(context.Background(), q, userID, mutation, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Status != "accepted" || outcome.Version != 2 {
		t.Fatalf("expected idempotent accepted outcome at version 2, got %+v", outcome)
	}
	if mock.queryRowCalls != 1 {
		t.Fatalf("expected only the GetSolveForUpdate query row call, got %d query row calls", mock.queryRowCalls)
	}
	if mock.execCalls != 0 {
		t.Fatalf("expected no exec calls (advisory lock is in Sync, not applySolve), got %d exec calls", mock.execCalls)
	}
}

func TestApplySessionDeleteVersionMismatchStillConflicts(t *testing.T) {
	t.Parallel()
	service := NewService(nil, 100, 100, 512*1024, 90*24*time.Hour)
	userID := uuid.New()
	sessionID := uuid.New()
	mock := &tombstoneMockDBTX{sessionRow: &storedb.CubeSession{
		ID: sessionID, UserID: userID, Name: "n", Event: "3x3", Kind: "manual",
		StartedAt: time.Now().UTC(), Version: 2, UpdatedAt: time.Now().UTC(),
	}}
	q := storedb.New(mock)

	mutation := Mutation{ID: uuid.New(), Entity: "session", EntityID: sessionID, Operation: "delete", BaseVersion: 1}
	outcome, err := service.applySession(context.Background(), q, userID, mutation, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Status != "conflict" || outcome.Version != 2 {
		t.Fatalf("expected conflict outcome at version 2, got %+v", outcome)
	}
}

func (m *tombstoneMockDBTX) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("unexpected CopyFrom")
}
