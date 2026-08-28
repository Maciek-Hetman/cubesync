-- name: SnapshotSessionsKeyset :many
SELECT id, user_id, name, event, kind, started_at, ended_at, archived, version, updated_at, deleted_at
FROM cube_sessions
WHERE user_id = $1 AND id > $2
ORDER BY id
LIMIT $3;

-- name: SnapshotSolvesKeyset :many
SELECT id, user_id, session_id, duration_ms, penalty, solved_at, scramble, event, version, updated_at, deleted_at
FROM solves
WHERE user_id = $1 AND id > $2
ORDER BY id
LIMIT $3;
