package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	storedb "github.com/Maciek-Hetman/cubing-sync-backend/internal/store/db"
	syncservice "github.com/Maciek-Hetman/cubing-sync-backend/internal/sync"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	defaultHistoryLimit = 50
	maxHistoryLimit     = 200
)

// listSessions handles GET /v1/sessions — keyset-paginated session list with solve counts.
func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	userID := principalFromContext(r.Context()).UserID
	limit := parseLimit(r, defaultHistoryLimit, maxHistoryLimit)

	beforeTs, beforeID, ok := parseCursor(r)
	if !ok {
		h.writeError(w, r, http.StatusBadRequest, "invalid_cursor", "cursor must be a timestamp,uuid pair")
		return
	}

	q := storedb.New(h.db)
	rows, err := q.ListSessionsPaginated(r.Context(), storedb.ListSessionsPaginatedParams{
		UserID:   userID,
		BeforeTs: beforeTs,
		BeforeID: beforeID,
		LimitVal: int32(limit + 1),
	})
	if err != nil {
		h.logger.Error("list_sessions_failed", "error", err)
		h.writeError(w, r, http.StatusInternalServerError, "internal_error", "could not list sessions")
		return
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	summaries := make([]syncservice.SessionSummary, 0, len(rows))
	var nextCursor string
	for _, row := range rows {
		s := syncservice.Session{
			ID:        row.ID,
			Name:      row.Name,
			Event:     row.Event,
			Kind:      row.Kind,
			StartedAt: row.StartedAt,
			EndedAt:   row.EndedAt,
			Archived:  row.Archived,
			Version:   row.Version,
			UpdatedAt: row.UpdatedAt,
			DeletedAt: row.DeletedAt,
		}
		summaries = append(summaries, syncservice.SessionSummary{Session: s, SolveCount: row.SolveCount})
		nextCursor = encodeCursor(row.StartedAt, row.ID)
	}

	if !hasMore {
		nextCursor = ""
	}

	writeJSON(w, http.StatusOK, syncservice.PaginatedSessionsResponse{
		Sessions:   summaries,
		NextCursor: nextCursor,
		HasMore:    hasMore,
	})
}

// listSessionSolves handles GET /v1/sessions/{id}/solves — keyset-paginated solves for a session.
func (h *Handler) listSessionSolves(w http.ResponseWriter, r *http.Request) {
	userID := principalFromContext(r.Context()).UserID
	sessionIDStr := chi.URLParam(r, "id")
	sessionID, err := uuid.Parse(sessionIDStr)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, "invalid_session_id", "session id must be a valid UUID")
		return
	}

	limit := parseLimit(r, defaultHistoryLimit, maxHistoryLimit)

	beforeTs, beforeID, ok := parseCursor(r)
	if !ok {
		h.writeError(w, r, http.StatusBadRequest, "invalid_cursor", "cursor must be a timestamp,uuid pair")
		return
	}

	q := storedb.New(h.db)
	rows, err := q.ListSolvesForSessionPaginated(r.Context(), storedb.ListSolvesForSessionPaginatedParams{
		UserID:    userID,
		SessionID: uuid.NullUUID{UUID: sessionID, Valid: true},
		BeforeTs:  beforeTs,
		BeforeID:  beforeID,
		LimitVal:  int32(limit + 1),
	})
	if err != nil {
		h.logger.Error("list_solves_failed", "error", err)
		h.writeError(w, r, http.StatusInternalServerError, "internal_error", "could not list solves")
		return
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	solves := make([]syncservice.Solve, 0, len(rows))
	var nextCursor string
	for _, row := range rows {
		var sid *uuid.UUID
		if row.SessionID.Valid {
			v := row.SessionID.UUID
			sid = &v
		}
		solve := syncservice.Solve{
			ID:         row.ID,
			SessionID:  sid,
			DurationMS: row.DurationMs,
			Penalty:    row.Penalty,
			SolvedAt:   row.SolvedAt,
			Scramble:   row.Scramble,
			Event:      row.Event,
			Version:    row.Version,
			UpdatedAt:  row.UpdatedAt,
			DeletedAt:  row.DeletedAt,
		}
		solves = append(solves, solve)
		nextCursor = encodeCursor(row.SolvedAt, row.ID)
	}

	if !hasMore {
		nextCursor = ""
	}

	writeJSON(w, http.StatusOK, syncservice.PaginatedSolvesResponse{
		Solves:     solves,
		NextCursor: nextCursor,
		HasMore:    hasMore,
	})
}

// parseCursor parses an opaque "RFC3339,uuid" cursor from the ?cursor= query param.
// Returns the far-future sentinel when no cursor is provided (first page).
func parseCursor(r *http.Request) (time.Time, uuid.UUID, bool) {
	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		// First page: use sentinel values that sort at the very end.
		return time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), uuid.Max, true
	}
	parts := strings.SplitN(raw, ",", 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, false
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	return ts, id, true
}

// encodeCursor serialises a (timestamp, uuid) keyset position to an opaque string.
func encodeCursor(ts time.Time, id uuid.UUID) string {
	return fmt.Sprintf("%s,%s", ts.UTC().Format(time.RFC3339Nano), id.String())
}

// parseLimit reads ?limit= from the request, clamping to [1, max].
func parseLimit(r *http.Request, def, max int) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}
