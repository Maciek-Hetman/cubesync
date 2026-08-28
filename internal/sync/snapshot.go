package sync

import (
	"context"
	"encoding/json"

	storedb "github.com/Maciek-Hetman/cubing-sync-backend/internal/store/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultSnapshotPageSize = 500
	maxSnapshotPageSize     = 2000

	entitySession = "session"
	entitySolve   = "solve"
)

// SnapshotService handles the bootstrap snapshot endpoint.
type SnapshotService struct {
	pool             *pgxpool.Pool
	maxResponseBytes int
}

// NewSnapshotService creates a SnapshotService.
func NewSnapshotService(pool *pgxpool.Pool, maxResponseBytes int) *SnapshotService {
	return &SnapshotService{pool: pool, maxResponseBytes: maxResponseBytes}
}

// Snapshot returns a page of materialized entity state for a new or full-resyncing device.
//
// Protocol:
//  1. Client sends {entity: "session", after_id: zero-uuid} for the first page.
//  2. When has_more is true for sessions, repeat with next_after_id.
//  3. When sessions are exhausted, next_entity is "solve" — client repeats from zero.
//  4. When has_more is false and next_entity is empty, bootstrap is complete.
//  5. The returned cursor is the change_log watermark at the start of bootstrap.
//     The client echoes it back on every page and uses it as the starting cursor
//     for incremental sync once bootstrap completes, so changes that land while
//     the snapshot is being streamed are caught by /v1/sync.
func (s *SnapshotService) Snapshot(ctx context.Context, userID uuid.UUID, req SnapshotRequest) (SnapshotResponse, error) {
	if req.Device.ID == uuid.Nil {
		return SnapshotResponse{}, clientError("invalid_device", "device.id is required")
	}
	if req.Entity != entitySession && req.Entity != entitySolve && req.Entity != "" {
		return SnapshotResponse{}, clientError("invalid_entity", "entity must be \"session\" or \"solve\"")
	}
	if req.Entity == "" {
		req.Entity = entitySession
	}

	pageSize := req.PageSize
	if pageSize <= 0 || pageSize > maxSnapshotPageSize {
		pageSize = defaultSnapshotPageSize
	}

	q := storedb.New(s.pool)

	// Upsert device so it gets tracked.
	if err := q.UpsertDevice(ctx, storedb.UpsertDeviceParams{
		ID: req.Device.ID, UserID: userID, Name: req.Device.Name, Platform: req.Device.Platform,
	}); err != nil {
		return SnapshotResponse{}, err
	}

	// Use the client-supplied watermark when present so the cursor stays stable
	// across pages. Compute it once on the first page otherwise.
	snapshotCursor := req.Cursor
	if snapshotCursor <= 0 {
		computed, err := q.LatestChangeCursor(ctx, userID)
		if err != nil {
			return SnapshotResponse{}, err
		}
		snapshotCursor = computed
	}

	switch req.Entity {
	case entitySession:
		return s.snapshotSessions(ctx, q, userID, req.AfterID, snapshotCursor, pageSize)
	case entitySolve:
		return s.snapshotSolves(ctx, q, userID, req.AfterID, snapshotCursor, pageSize)
	default:
		return SnapshotResponse{}, clientError("invalid_entity", "entity must be \"session\" or \"solve\"")
	}
}

func (s *SnapshotService) snapshotSessions(
	ctx context.Context, q *storedb.Queries,
	userID, afterID uuid.UUID,
	snapshotCursor int64,
	pageSize int,
) (SnapshotResponse, error) {
	rows, err := q.SnapshotSessionsKeyset(ctx, storedb.SnapshotSessionsKeysetParams{
		UserID: userID, ID: afterID, Limit: int32(pageSize + 1),
	})
	if err != nil {
		return SnapshotResponse{}, err
	}

	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}

	sessions := make([]Session, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, sessionFromDB(row))
	}

	if s.maxResponseBytes > 0 {
		var trimmed bool
		sessions, trimmed = trimSessionsByBytes(sessions, s.maxResponseBytes)
		if trimmed {
			hasMore = true
		}
	}

	var nextAfterID uuid.UUID
	if len(sessions) > 0 {
		nextAfterID = sessions[len(sessions)-1].ID
	}

	resp := SnapshotResponse{
		Sessions: sessions,
		Cursor:   snapshotCursor,
		HasMore:  hasMore,
	}
	if hasMore {
		resp.NextEntity = entitySession
		resp.NextAfterID = nextAfterID
	} else {
		// Sessions exhausted — tell client to start fetching solves.
		resp.NextEntity = entitySolve
	}
	return resp, nil
}

func (s *SnapshotService) snapshotSolves(
	ctx context.Context, q *storedb.Queries,
	userID, afterID uuid.UUID,
	snapshotCursor int64,
	pageSize int,
) (SnapshotResponse, error) {
	rows, err := q.SnapshotSolvesKeyset(ctx, storedb.SnapshotSolvesKeysetParams{
		UserID: userID, ID: afterID, Limit: int32(pageSize + 1),
	})
	if err != nil {
		return SnapshotResponse{}, err
	}

	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}

	solves := make([]Solve, 0, len(rows))
	for _, row := range rows {
		solves = append(solves, solveFromDB(row))
	}

	if s.maxResponseBytes > 0 {
		var trimmed bool
		solves, trimmed = trimSolvesByBytes(solves, s.maxResponseBytes)
		if trimmed {
			hasMore = true
		}
	}

	var nextAfterID uuid.UUID
	if len(solves) > 0 {
		nextAfterID = solves[len(solves)-1].ID
	}

	resp := SnapshotResponse{
		Solves:  solves,
		Cursor:  snapshotCursor,
		HasMore: hasMore,
	}
	if hasMore {
		resp.NextEntity = entitySolve
		resp.NextAfterID = nextAfterID
	}
	// When has_more == false and NextEntity is empty, bootstrap is complete.
	return resp, nil
}

// entityEnvelopeOverhead is a conservative estimate of the JSON envelope bytes
// surrounding each snapshot entity (UUIDs, version, timestamps).
const entityEnvelopeOverhead = 120

func trimSessionsByBytes(sessions []Session, budget int) ([]Session, bool) {
	if budget <= 0 || len(sessions) <= 1 {
		return sessions, false
	}
	total := 0
	keep := 0
	for i, session := range sessions {
		raw, err := json.Marshal(session)
		if err != nil {
			continue
		}
		size := len(raw) + entityEnvelopeOverhead
		if total+size > budget && i > 0 {
			break
		}
		total += size
		keep = i + 1
	}
	return sessions[:keep], keep < len(sessions)
}

func trimSolvesByBytes(solves []Solve, budget int) ([]Solve, bool) {
	if budget <= 0 || len(solves) <= 1 {
		return solves, false
	}
	total := 0
	keep := 0
	for i, solve := range solves {
		raw, err := json.Marshal(solve)
		if err != nil {
			continue
		}
		size := len(raw) + entityEnvelopeOverhead
		if total+size > budget && i > 0 {
			break
		}
		total += size
		keep = i + 1
	}
	return solves[:keep], keep < len(solves)
}
