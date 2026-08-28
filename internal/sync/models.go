package sync

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Request struct {
	Cursor    int64      `json:"cursor"`
	Device    Device     `json:"device"`
	Mutations []Mutation `json:"mutations"`
	Limit     int        `json:"limit,omitempty"`
}

type Device struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Platform string    `json:"platform"`
}

type Mutation struct {
	ID          uuid.UUID       `json:"id"`
	Entity      string          `json:"entity"`
	EntityID    uuid.UUID       `json:"entity_id"`
	Operation   string          `json:"operation"`
	BaseVersion int64           `json:"base_version"`
	Data        json.RawMessage `json:"data,omitempty"`
}

type Session struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	Event     string     `json:"event"`
	Kind      string     `json:"kind"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Archived  bool       `json:"archived"`
	Version   int64      `json:"version"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

type Solve struct {
	ID         uuid.UUID  `json:"id"`
	SessionID  *uuid.UUID `json:"session_id,omitempty"`
	DurationMS int64      `json:"duration_ms"`
	Penalty    string     `json:"penalty"`
	SolvedAt   time.Time  `json:"solved_at"`
	Scramble   string     `json:"scramble"`
	Event      string     `json:"event"`
	Version    int64      `json:"version"`
	UpdatedAt  time.Time  `json:"updated_at"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
}

type MutationOutcome struct {
	MutationID uuid.UUID       `json:"mutation_id"`
	Status     string          `json:"status"`
	Version    int64           `json:"version,omitempty"`
	Code       string          `json:"code,omitempty"`
	Message    string          `json:"message,omitempty"`
	Current    json.RawMessage `json:"current,omitempty"`
}

type Change struct {
	Cursor    int64           `json:"cursor"`
	Entity    string          `json:"entity"`
	EntityID  uuid.UUID       `json:"entity_id"`
	Operation string          `json:"operation"`
	Version   int64           `json:"version"`
	Data      json.RawMessage `json:"data"`
	ChangedAt time.Time       `json:"changed_at"`
}

type Response struct {
	Outcomes   []MutationOutcome `json:"outcomes"`
	Changes    []Change          `json:"changes"`
	NextCursor int64             `json:"next_cursor"`
	HasMore    bool              `json:"has_more"`
}

// SnapshotRequest is the request for bootstrap snapshot.
type SnapshotRequest struct {
	Device   Device    `json:"device"`
	Cursor   int64     `json:"cursor"`   // change-log watermark; 0 on the first page, echo the response cursor after that
	AfterID  uuid.UUID `json:"after_id"` // zero UUID for the first page of each entity
	Entity   string    `json:"entity"`   // "session" or "solve"
	PageSize int       `json:"page_size,omitempty"`
}

// SnapshotResponse returns a page of materialized entity state.
type SnapshotResponse struct {
	Sessions    []Session `json:"sessions,omitempty"`
	Solves      []Solve   `json:"solves,omitempty"`
	Cursor      int64     `json:"cursor"`
	HasMore     bool      `json:"has_more"`
	NextEntity  string    `json:"next_entity,omitempty"`
	NextAfterID uuid.UUID `json:"next_after_id,omitempty"`
}

// DeleteStub is the slim payload for delete changes under protocol v2.
type DeleteStub struct {
	ID        uuid.UUID  `json:"id"`
	Version   int64      `json:"version"`
	DeletedAt *time.Time `json:"deleted_at"`
}

// ConflictStub is the slim current-entity payload in conflict outcomes under protocol v2.
type ConflictStub struct {
	ID        uuid.UUID `json:"id"`
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

// StatsRequest is the request for server-side statistics.
type StatsRequest struct {
	Event string `json:"event"` // optional filter, empty = all events
}

// StatsResponse contains aggregated solve statistics.
type StatsResponse struct {
	TotalCount   int64   `json:"total_count"`
	CountedCount int64   `json:"counted_count"` // excluding DNF
	DNFCount     int64   `json:"dnf_count"`
	MinMS        int64   `json:"min_ms"`
	MaxMS        int64   `json:"max_ms"`
	MeanMS       float64 `json:"mean_ms"`
	StddevMS     float64 `json:"stddev_ms"`
	TotalMS      int64   `json:"total_ms"`
	Ao5          *int64  `json:"ao5,omitempty"`
	Ao12         *int64  `json:"ao12,omitempty"`
	Ao50         *int64  `json:"ao50,omitempty"`
	Ao100        *int64  `json:"ao100,omitempty"`
}

// SessionSummary is a session with a solve count for paginated history.
type SessionSummary struct {
	Session
	SolveCount int64 `json:"solve_count"`
}

// PaginatedSessionsResponse is the response for paginated session history.
type PaginatedSessionsResponse struct {
	Sessions   []SessionSummary `json:"sessions"`
	NextCursor string           `json:"next_cursor,omitempty"` // opaque "timestamp,uuid" string
	HasMore    bool             `json:"has_more"`
}

// PaginatedSolvesResponse is the response for paginated solve history.
type PaginatedSolvesResponse struct {
	Solves     []Solve `json:"solves"`
	NextCursor string  `json:"next_cursor,omitempty"` // opaque "timestamp,uuid" string
	HasMore    bool    `json:"has_more"`
}
