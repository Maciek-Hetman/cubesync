-- name: ListSessionsPaginated :many
SELECT
    cs.id, cs.user_id, cs.name, cs.event, cs.kind, cs.started_at, cs.ended_at,
    cs.archived, cs.version, cs.updated_at, cs.deleted_at,
    (SELECT COUNT(*)::bigint FROM solves s
     WHERE s.user_id = cs.user_id AND s.session_id = cs.id AND s.deleted_at IS NULL
    ) AS solve_count
FROM cube_sessions cs
WHERE cs.user_id = $1
    AND cs.deleted_at IS NULL
    AND (cs.started_at < sqlc.arg(before_ts)::timestamptz
         OR (cs.started_at = sqlc.arg(before_ts)::timestamptz AND cs.id < sqlc.arg(before_id)::uuid))
ORDER BY cs.started_at DESC, cs.id DESC
LIMIT sqlc.arg(limit_val);

-- name: ListSolvesForSessionPaginated :many
SELECT id, user_id, session_id, duration_ms, penalty, solved_at, scramble, event, version, updated_at, deleted_at
FROM solves
WHERE user_id = $1
    AND session_id = $2
    AND deleted_at IS NULL
    AND (solved_at < sqlc.arg(before_ts)::timestamptz
         OR (solved_at = sqlc.arg(before_ts)::timestamptz AND id < sqlc.arg(before_id)::uuid))
ORDER BY solved_at DESC, id DESC
LIMIT sqlc.arg(limit_val);
