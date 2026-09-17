package sync

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	storedb "github.com/Maciek-Hetman/cubing-sync-backend/internal/store/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fixedMinValidDBTX implements storedb.DBTX and answers MinValidCursorForUser
// (a :one query scanning a single int64 column) with a fixed value or error,
// regardless of the query text or arguments. It lets checkCursorNotExpired be
// exercised directly without a real database.
type fixedMinValidDBTX struct {
	minValid int64
	err      error
}

func (m *fixedMinValidDBTX) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (m *fixedMinValidDBTX) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, nil
}

func (m *fixedMinValidDBTX) QueryRow(context.Context, string, ...interface{}) pgx.Row {
	return &fixedMinValidRow{value: m.minValid, err: m.err}
}

type fixedMinValidRow struct {
	value int64
	err   error
}

func (r *fixedMinValidRow) Scan(dest ...interface{}) error {
	if r.err != nil {
		return r.err
	}
	ptr, ok := dest[0].(*int64)
	if !ok {
		return fmt.Errorf("unexpected scan destination %T", dest[0])
	}
	*ptr = r.value
	return nil
}

func TestCheckCursorNotExpiredSkipsWhenCursorIsZeroOrNegative(t *testing.T) {
	t.Parallel()
	service := NewService(nil, 100, 100, 512*1024, 90*24*time.Hour)
	// minValid is deliberately above any cursor we pass, so a non-skipping
	// implementation would report cursor_expired here.
	q := storedb.New(&fixedMinValidDBTX{minValid: 1000})

	for _, cursor := range []int64{0, -1} {
		if err := service.checkCursorNotExpired(context.Background(), q, uuid.New(), cursor); err != nil {
			t.Fatalf("cursor %d: expected no check for non-positive cursor, got %v", cursor, err)
		}
	}
}

func TestCheckCursorNotExpiredFlagsStaleCursor(t *testing.T) {
	t.Parallel()
	service := NewService(nil, 100, 100, 512*1024, 90*24*time.Hour)
	q := storedb.New(&fixedMinValidDBTX{minValid: 50})

	err := service.checkCursorNotExpired(context.Background(), q, uuid.New(), 49)
	var clientErr ClientError
	if !errors.As(err, &clientErr) || clientErr.Code != "cursor_expired" {
		t.Fatalf("expected cursor_expired, got %v", err)
	}
}

func TestCheckCursorNotExpiredAllowsCurrentCursor(t *testing.T) {
	t.Parallel()
	service := NewService(nil, 100, 100, 512*1024, 90*24*time.Hour)
	q := storedb.New(&fixedMinValidDBTX{minValid: 50})

	if err := service.checkCursorNotExpired(context.Background(), q, uuid.New(), 50); err != nil {
		t.Fatalf("cursor at the floor must not be rejected: %v", err)
	}
	if err := service.checkCursorNotExpired(context.Background(), q, uuid.New(), 51); err != nil {
		t.Fatalf("cursor above the floor must not be rejected: %v", err)
	}
}

func TestCheckCursorNotExpiredAllowsAnyCursorWhenNoActiveDevices(t *testing.T) {
	t.Parallel()
	service := NewService(nil, 100, 100, 512*1024, 90*24*time.Hour)
	// MinValidCursorForUser returns 0 (via COALESCE) when there are no
	// devices within the window; that must never be treated as a floor.
	q := storedb.New(&fixedMinValidDBTX{minValid: 0})

	if err := service.checkCursorNotExpired(context.Background(), q, uuid.New(), 1); err != nil {
		t.Fatalf("a zero floor must never expire a cursor: %v", err)
	}
}

// TestCheckCursorNotExpiredWrapsDBErrorInsteadOfSwallowingIt guards against
// the original bug where `err == nil && ...` silently let a request through
// whenever MinValidCursorForUser failed, instead of surfacing the failure.
func TestCheckCursorNotExpiredWrapsDBErrorInsteadOfSwallowingIt(t *testing.T) {
	t.Parallel()
	service := NewService(nil, 100, 100, 512*1024, 90*24*time.Hour)
	q := storedb.New(&fixedMinValidDBTX{err: errSimulatedDB})

	err := service.checkCursorNotExpired(context.Background(), q, uuid.New(), 1)
	if err == nil {
		t.Fatal("expected the DB error to be surfaced, got nil")
	}
	if !errors.Is(err, errSimulatedDB) {
		t.Fatalf("expected the DB root cause to be preserved via %%w, got %v", err)
	}
	var clientErr ClientError
	if errors.As(err, &clientErr) {
		t.Fatalf("a DB failure must not be reported as cursor_expired (that implies checked-and-fine): %v", err)
	}
}

func TestEffectiveInactiveWindowFallsBackToDefault(t *testing.T) {
	t.Parallel()
	for _, d := range []time.Duration{0, -1, -24 * time.Hour} {
		if got := effectiveInactiveWindow(d); got != defaultInactiveDeviceWindow {
			t.Fatalf("effectiveInactiveWindow(%v) = %v, want default %v", d, got, defaultInactiveDeviceWindow)
		}
	}
}

func TestEffectiveInactiveWindowKeepsPositiveValue(t *testing.T) {
	t.Parallel()
	want := 7 * 24 * time.Hour
	if got := effectiveInactiveWindow(want); got != want {
		t.Fatalf("effectiveInactiveWindow(%v) = %v, want %v unchanged", want, got, want)
	}
}

func (m *fixedMinValidDBTX) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("unexpected CopyFrom")
}
