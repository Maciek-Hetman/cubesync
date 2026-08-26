package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"

	storedb "github.com/Maciek-Hetman/cubing-sync-backend/internal/store/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxSolveDurationMS = 24 * 60 * 60 * 1000

type Service struct {
	pool             *pgxpool.Pool
	maxMutations     int
	defaultMaxChange int
}

func NewService(pool *pgxpool.Pool, maxMutations, defaultMaxChange int) *Service {
	return &Service{pool: pool, maxMutations: maxMutations, defaultMaxChange: defaultMaxChange}
}

func (s *Service) Sync(ctx context.Context, userID uuid.UUID, req Request) (Response, error) {
	if req.Cursor < 0 {
		return Response{}, clientError("invalid_cursor", "cursor cannot be negative")
	}
	if req.Device.ID == uuid.Nil {
		return Response{}, clientError("invalid_device", "device.id is required")
	}
	if len(req.Device.Name) > 120 || len(req.Device.Platform) > 40 {
		return Response{}, clientError("invalid_device", "device metadata is too long")
	}
	if len(req.Mutations) > s.maxMutations {
		return Response{}, clientError("too_many_mutations", fmt.Sprintf("at most %d mutations are allowed", s.maxMutations))
	}
	seenMutationIDs := make(map[uuid.UUID]struct{}, len(req.Mutations))
	for _, mutation := range req.Mutations {
		if mutation.ID == uuid.Nil {
			return Response{}, clientError("invalid_mutation_id", "every mutation id must be a non-zero UUID")
		}
		if _, exists := seenMutationIDs[mutation.ID]; exists {
			return Response{}, clientError("duplicate_mutation_id", "mutation ids must be unique within a request")
		}
		seenMutationIDs[mutation.ID] = struct{}{}
	}

	limit := req.Limit
	if limit <= 0 || limit > s.defaultMaxChange {
		limit = s.defaultMaxChange
	}

	if len(req.Mutations) == 0 {
		return s.pullOnly(ctx, userID, req, limit)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := storedb.New(tx)

	if err := q.UpsertDevice(ctx, storedb.UpsertDeviceParams{
		ID: req.Device.ID, UserID: userID, Name: req.Device.Name, Platform: req.Device.Platform,
	}); err != nil {
		return Response{}, err
	}

	outcomeByID := make(map[uuid.UUID]MutationOutcome, len(req.Mutations))
	ordered := append([]Mutation(nil), req.Mutations...)
	slices.SortStableFunc(ordered, func(a, b Mutation) int {
		if a.Entity != b.Entity {
			if a.Entity == "session" {
				return -1
			}
			return 1
		}
		return strings.Compare(a.EntityID.String(), b.EntityID.String())
	})

	for _, mutation := range ordered {
		outcome, err := s.applyMutation(ctx, q, userID, req.Device.ID, mutation)
		if err != nil {
			return Response{}, err
		}
		outcomeByID[mutation.ID] = outcome
	}

	outcomes := make([]MutationOutcome, 0, len(req.Mutations))
	for _, mutation := range req.Mutations {
		outcomes = append(outcomes, outcomeByID[mutation.ID])
	}

	rows, err := q.ListChanges(ctx, storedb.ListChangesParams{
		UserID: userID, ChangeID: req.Cursor, Limit: int32(limit + 1),
	})
	if err != nil {
		return Response{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	changes := make([]Change, 0, len(rows))
	nextCursor := req.Cursor
	for _, row := range rows {
		changes = append(changes, Change{
			Cursor: row.ChangeID, Entity: row.EntityType, EntityID: row.EntityID,
			Operation: row.Operation, Version: row.Version, Data: row.Payload, ChangedAt: row.CreatedAt,
		})
		nextCursor = row.ChangeID
	}

	if err := tx.Commit(ctx); err != nil {
		return Response{}, err
	}
	return Response{Outcomes: outcomes, Changes: changes, NextCursor: nextCursor, HasMore: hasMore}, nil
}

func (s *Service) pullOnly(ctx context.Context, userID uuid.UUID, req Request, limit int) (Response, error) {
	q := storedb.New(s.pool)

	if err := q.UpsertDevice(ctx, storedb.UpsertDeviceParams{
		ID: req.Device.ID, UserID: userID, Name: req.Device.Name, Platform: req.Device.Platform,
	}); err != nil {
		return Response{}, err
	}

	rows, err := q.ListChanges(ctx, storedb.ListChangesParams{
		UserID: userID, ChangeID: req.Cursor, Limit: int32(limit + 1),
	})
	if err != nil {
		return Response{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	changes := make([]Change, 0, len(rows))
	nextCursor := req.Cursor
	for _, row := range rows {
		changes = append(changes, Change{
			Cursor: row.ChangeID, Entity: row.EntityType, EntityID: row.EntityID,
			Operation: row.Operation, Version: row.Version, Data: row.Payload, ChangedAt: row.CreatedAt,
		})
		nextCursor = row.ChangeID
	}
	return Response{Outcomes: []MutationOutcome{}, Changes: changes, NextCursor: nextCursor, HasMore: hasMore}, nil
}

func (s *Service) applyMutation(
	ctx context.Context,
	q *storedb.Queries,
	userID, deviceID uuid.UUID,
	m Mutation,
) (MutationOutcome, error) {
	if m.ID != uuid.Nil {
		if err := q.AcquireAdvisoryLockByID(ctx, advisoryLockKey(userID.String(), deviceID.String(), "mutation", m.ID.String())); err != nil {
			return MutationOutcome{}, err
		}
		raw, err := q.GetProcessedMutation(ctx, storedb.GetProcessedMutationParams{
			UserID: userID, DeviceID: deviceID, MutationID: m.ID,
		})
		if err == nil {
			var stored MutationOutcome
			if err := json.Unmarshal(raw, &stored); err != nil {
				return MutationOutcome{}, err
			}
			return stored, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return MutationOutcome{}, err
		}
	}

	var outcome MutationOutcome
	switch m.Entity {
	case "session":
		outcome = s.applySession(ctx, q, userID, m)
	case "solve":
		outcome = s.applySolve(ctx, q, userID, m)
	default:
		outcome = rejected(m.ID, "invalid_entity", "entity must be session or solve")
	}
	if outcome.Status == "internal_error" {
		return MutationOutcome{}, errors.New(outcome.Message)
	}

	if m.ID != uuid.Nil {
		raw, err := json.Marshal(outcome)
		if err != nil {
			return MutationOutcome{}, err
		}
		if err := q.RecordProcessedMutation(ctx, storedb.RecordProcessedMutationParams{
			UserID: userID, DeviceID: deviceID, MutationID: m.ID, Outcome: raw,
		}); err != nil {
			return MutationOutcome{}, err
		}
	}
	return outcome, nil
}

func (s *Service) applySession(ctx context.Context, q *storedb.Queries, userID uuid.UUID, m Mutation) MutationOutcome {
	if outcome := validateMutationEnvelope(m); outcome != nil {
		return *outcome
	}
	if err := q.AcquireAdvisoryLockByID(ctx, advisoryLockKey(userID.String(), "session", m.EntityID.String())); err != nil {
		return internal(m.ID, err)
	}
	current, currentErr := q.GetSessionForUpdate(ctx, storedb.GetSessionForUpdateParams{UserID: userID, ID: m.EntityID})
	if currentErr != nil && !errors.Is(currentErr, pgx.ErrNoRows) {
		return internal(m.ID, currentErr)
	}

	if m.Operation == "delete" {
		if errors.Is(currentErr, pgx.ErrNoRows) {
			return rejected(m.ID, "not_found", "session does not exist")
		}
		if current.Version != m.BaseVersion {
			return sessionConflict(m.ID, current)
		}
		deleted, err := q.DeleteSession(ctx, storedb.DeleteSessionParams{
			UserID: userID, ID: m.EntityID, Version: m.BaseVersion,
		})
		if err != nil {
			return internal(m.ID, err)
		}
		if err := appendSessionChange(ctx, q, userID, "delete", deleted); err != nil {
			return internal(m.ID, err)
		}
		return accepted(m.ID, deleted.Version)
	}

	var input Session
	if err := json.Unmarshal(m.Data, &input); err != nil {
		return rejected(m.ID, "invalid_session", "session data is invalid")
	}
	if message := validateSession(input, m.EntityID); message != "" {
		return rejected(m.ID, "invalid_session", message)
	}

	var result storedb.CubeSession
	var err error
	if errors.Is(currentErr, pgx.ErrNoRows) {
		if m.BaseVersion != 0 {
			return rejected(m.ID, "not_found", "session does not exist")
		}
		result, err = q.InsertSession(ctx, storedb.InsertSessionParams{
			ID: input.ID, UserID: userID, Name: input.Name, Event: input.Event, Kind: input.Kind,
			StartedAt: input.StartedAt, EndedAt: input.EndedAt, Archived: input.Archived,
		})
	} else {
		if current.Version != m.BaseVersion {
			return sessionConflict(m.ID, current)
		}
		result, err = q.UpdateSession(ctx, storedb.UpdateSessionParams{
			UserID: userID, ID: input.ID, Name: input.Name, Event: input.Event, Kind: input.Kind,
			StartedAt: input.StartedAt, EndedAt: input.EndedAt, Archived: input.Archived, Version: m.BaseVersion,
		})
	}
	if err != nil {
		return internal(m.ID, err)
	}
	if err := appendSessionChange(ctx, q, userID, "upsert", result); err != nil {
		return internal(m.ID, err)
	}
	return accepted(m.ID, result.Version)
}

func (s *Service) applySolve(ctx context.Context, q *storedb.Queries, userID uuid.UUID, m Mutation) MutationOutcome {
	if outcome := validateMutationEnvelope(m); outcome != nil {
		return *outcome
	}
	if err := q.AcquireAdvisoryLockByID(ctx, advisoryLockKey(userID.String(), "solve", m.EntityID.String())); err != nil {
		return internal(m.ID, err)
	}
	current, currentErr := q.GetSolveForUpdate(ctx, storedb.GetSolveForUpdateParams{UserID: userID, ID: m.EntityID})
	if currentErr != nil && !errors.Is(currentErr, pgx.ErrNoRows) {
		return internal(m.ID, currentErr)
	}

	if m.Operation == "delete" {
		if errors.Is(currentErr, pgx.ErrNoRows) {
			return rejected(m.ID, "not_found", "solve does not exist")
		}
		if current.Version != m.BaseVersion {
			return solveConflict(m.ID, current)
		}
		deleted, err := q.DeleteSolve(ctx, storedb.DeleteSolveParams{
			UserID: userID, ID: m.EntityID, Version: m.BaseVersion,
		})
		if err != nil {
			return internal(m.ID, err)
		}
		if err := appendSolveChange(ctx, q, userID, "delete", deleted); err != nil {
			return internal(m.ID, err)
		}
		return accepted(m.ID, deleted.Version)
	}

	var input Solve
	if err := json.Unmarshal(m.Data, &input); err != nil {
		return rejected(m.ID, "invalid_solve", "solve data is invalid")
	}
	if message := validateSolve(input, m.EntityID); message != "" {
		return rejected(m.ID, "invalid_solve", message)
	}
	sessionID := uuid.NullUUID{}
	if input.SessionID != nil {
		sessionID = uuid.NullUUID{UUID: *input.SessionID, Valid: true}
	}

	var result storedb.Solf
	var err error
	if errors.Is(currentErr, pgx.ErrNoRows) {
		if m.BaseVersion != 0 {
			return rejected(m.ID, "not_found", "solve does not exist")
		}
		result, err = q.InsertSolve(ctx, storedb.InsertSolveParams{
			ID: input.ID, UserID: userID, SessionID: sessionID, DurationMs: input.DurationMS,
			Penalty: input.Penalty, SolvedAt: input.SolvedAt, Scramble: input.Scramble, Event: input.Event,
		})
	} else {
		if current.Version != m.BaseVersion {
			return solveConflict(m.ID, current)
		}
		result, err = q.UpdateSolve(ctx, storedb.UpdateSolveParams{
			UserID: userID, ID: input.ID, SessionID: sessionID, DurationMs: input.DurationMS,
			Penalty: input.Penalty, SolvedAt: input.SolvedAt, Scramble: input.Scramble,
			Event: input.Event, Version: m.BaseVersion,
		})
	}
	if err != nil {
		if isForeignKeyViolation(err) {
			return rejected(m.ID, "invalid_session", "referenced session does not exist")
		}
		return internal(m.ID, err)
	}
	if err := appendSolveChange(ctx, q, userID, "upsert", result); err != nil {
		return internal(m.ID, err)
	}
	return accepted(m.ID, result.Version)
}

func validateMutationEnvelope(m Mutation) *MutationOutcome {
	if m.ID == uuid.Nil {
		outcome := rejected(m.ID, "invalid_mutation_id", "mutation id is required")
		return &outcome
	}
	if m.EntityID == uuid.Nil {
		outcome := rejected(m.ID, "invalid_entity_id", "entity id is required")
		return &outcome
	}
	if m.BaseVersion < 0 {
		outcome := rejected(m.ID, "invalid_version", "base_version cannot be negative")
		return &outcome
	}
	if m.Operation != "upsert" && m.Operation != "delete" {
		outcome := rejected(m.ID, "invalid_operation", "operation must be upsert or delete")
		return &outcome
	}
	return nil
}

func validateSession(value Session, expectedID uuid.UUID) string {
	if value.ID != expectedID {
		return "data.id must match entity_id"
	}
	if len(value.Name) > 120 {
		return "name is too long"
	}
	if !validEvent(value.Event) {
		return "event is not supported"
	}
	if value.Kind != "manual" && value.Kind != "automatic" {
		return "kind must be manual or automatic"
	}
	if value.StartedAt.IsZero() {
		return "started_at is required"
	}
	if value.EndedAt != nil && value.EndedAt.Before(value.StartedAt) {
		return "ended_at cannot be before started_at"
	}
	return ""
}

func validateSolve(value Solve, expectedID uuid.UUID) string {
	if value.ID != expectedID {
		return "data.id must match entity_id"
	}
	if value.DurationMS < 0 || value.DurationMS > maxSolveDurationMS {
		return "duration_ms is outside the supported range"
	}
	if value.Penalty != "none" && value.Penalty != "plus_two" && value.Penalty != "dnf" {
		return "penalty must be none, plus_two, or dnf"
	}
	if value.SolvedAt.IsZero() {
		return "solved_at is required"
	}
	if len(value.Scramble) > 4096 {
		return "scramble is too long"
	}
	if !validEvent(value.Event) {
		return "event is not supported"
	}
	return ""
}

func validEvent(value string) bool {
	switch strings.ToLower(value) {
	case "2x2", "3x3", "4x4", "5x5", "megaminx", "pyraminx":
		return true
	default:
		return false
	}
}

func appendSessionChange(ctx context.Context, q *storedb.Queries, userID uuid.UUID, operation string, row storedb.CubeSession) error {
	payload, err := json.Marshal(sessionFromDB(row))
	if err != nil {
		return err
	}
	_, err = q.AppendChange(ctx, storedb.AppendChangeParams{
		UserID: userID, EntityType: "session", EntityID: row.ID, Operation: operation,
		Version: row.Version, Payload: payload,
	})
	return err
}

func appendSolveChange(ctx context.Context, q *storedb.Queries, userID uuid.UUID, operation string, row storedb.Solf) error {
	payload, err := json.Marshal(solveFromDB(row))
	if err != nil {
		return err
	}
	_, err = q.AppendChange(ctx, storedb.AppendChangeParams{
		UserID: userID, EntityType: "solve", EntityID: row.ID, Operation: operation,
		Version: row.Version, Payload: payload,
	})
	return err
}

func sessionFromDB(row storedb.CubeSession) Session {
	return Session{
		ID: row.ID, Name: row.Name, Event: row.Event, Kind: row.Kind, StartedAt: row.StartedAt,
		EndedAt: row.EndedAt, Archived: row.Archived, Version: row.Version,
		UpdatedAt: row.UpdatedAt, DeletedAt: row.DeletedAt,
	}
}

func solveFromDB(row storedb.Solf) Solve {
	var sessionID *uuid.UUID
	if row.SessionID.Valid {
		value := row.SessionID.UUID
		sessionID = &value
	}
	return Solve{
		ID: row.ID, SessionID: sessionID, DurationMS: row.DurationMs, Penalty: row.Penalty,
		SolvedAt: row.SolvedAt, Scramble: row.Scramble, Event: row.Event, Version: row.Version,
		UpdatedAt: row.UpdatedAt, DeletedAt: row.DeletedAt,
	}
}

func sessionConflict(mutationID uuid.UUID, current storedb.CubeSession) MutationOutcome {
	raw, _ := json.Marshal(sessionFromDB(current))
	return MutationOutcome{MutationID: mutationID, Status: "conflict", Version: current.Version, Current: raw}
}

func solveConflict(mutationID uuid.UUID, current storedb.Solf) MutationOutcome {
	raw, _ := json.Marshal(solveFromDB(current))
	return MutationOutcome{MutationID: mutationID, Status: "conflict", Version: current.Version, Current: raw}
}

func accepted(id uuid.UUID, version int64) MutationOutcome {
	return MutationOutcome{MutationID: id, Status: "accepted", Version: version}
}

func rejected(id uuid.UUID, code, message string) MutationOutcome {
	return MutationOutcome{MutationID: id, Status: "rejected", Code: code, Message: message}
}

func internal(id uuid.UUID, err error) MutationOutcome {
	return MutationOutcome{MutationID: id, Status: "internal_error", Message: err.Error()}
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func advisoryLockKey(parts ...string) int64 {
	h := fnv.New64a()
	for _, p := range parts {
		h.Write([]byte(p))
	}
	return int64(h.Sum64())
}

type ClientError struct {
	Code    string
	Message string
}

func (e ClientError) Error() string { return e.Message }

func clientError(code, message string) error {
	return ClientError{Code: code, Message: message}
}
