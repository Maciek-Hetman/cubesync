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
