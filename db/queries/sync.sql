-- name: AcquireAdvisoryLock :exec
SELECT pg_advisory_xact_lock(hashtextextended($1, 0));

-- name: UpsertDevice :exec
INSERT INTO devices (id, user_id, name, platform)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, id) DO UPDATE
SET name = EXCLUDED.name,
    platform = EXCLUDED.platform,
    last_seen_at = now();

-- name: GetProcessedMutation :one
SELECT outcome FROM processed_mutations
WHERE user_id = $1 AND device_id = $2 AND mutation_id = $3;

-- name: RecordProcessedMutation :exec
INSERT INTO processed_mutations (user_id, device_id, mutation_id, outcome)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, device_id, mutation_id) DO NOTHING;

-- name: GetSessionForUpdate :one
SELECT * FROM cube_sessions
WHERE user_id = $1 AND id = $2
FOR UPDATE;

-- name: GetLiveSession :one
SELECT * FROM cube_sessions
WHERE user_id = $1 AND id = $2 AND deleted_at IS NULL;

-- name: InsertSession :one
INSERT INTO cube_sessions (
    id, user_id, name, event, kind, started_at, ended_at, archived
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: UpdateSession :one
UPDATE cube_sessions
SET name = $3,
    event = $4,
    kind = $5,
    started_at = $6,
    ended_at = $7,
    archived = $8,
    version = version + 1,
    updated_at = now(),
    deleted_at = NULL
WHERE user_id = $1 AND id = $2 AND version = $9
RETURNING *;

-- name: DeleteSession :one
UPDATE cube_sessions
SET version = version + 1, updated_at = now(), deleted_at = now()
WHERE user_id = $1 AND id = $2 AND version = $3 AND deleted_at IS NULL
RETURNING *;

-- name: GetSolveForUpdate :one
SELECT * FROM solves
WHERE user_id = $1 AND id = $2
FOR UPDATE;

-- name: InsertSolve :one
INSERT INTO solves (
    id, user_id, session_id, duration_ms, penalty, solved_at, scramble, event
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: UpdateSolve :one
UPDATE solves
SET session_id = $3,
    duration_ms = $4,
    penalty = $5,
    solved_at = $6,
    scramble = $7,
    event = $8,
    version = version + 1,
    updated_at = now(),
    deleted_at = NULL
WHERE user_id = $1 AND id = $2 AND version = $9
RETURNING *;

-- name: DeleteSolve :one
UPDATE solves
SET version = version + 1, updated_at = now(), deleted_at = now()
WHERE user_id = $1 AND id = $2 AND version = $3 AND deleted_at IS NULL
RETURNING *;

-- name: AppendChange :one
INSERT INTO change_log (
    user_id, entity_type, entity_id, operation, version, payload
) VALUES ($1, $2, $3, $4, $5, $6)
RETURNING change_id;

-- name: ListChanges :many
SELECT * FROM change_log
WHERE user_id = $1 AND change_id > $2
ORDER BY change_id
LIMIT $3;

-- name: LatestChangeCursor :one
SELECT COALESCE(MAX(change_id), 0)::bigint FROM change_log
WHERE user_id = $1;
