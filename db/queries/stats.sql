-- name: UserSolveStats :one
SELECT
    COUNT(*)::bigint AS total_count,
    COUNT(*) FILTER (WHERE penalty != 'dnf')::bigint AS counted_count,
    COALESCE(MIN(
        CASE WHEN penalty = 'plus_two' THEN duration_ms + 2000 ELSE duration_ms END
    ) FILTER (WHERE penalty != 'dnf'), 0)::bigint AS min_ms,
    COALESCE(MAX(
        CASE WHEN penalty = 'plus_two' THEN duration_ms + 2000 ELSE duration_ms END
    ) FILTER (WHERE penalty != 'dnf'), 0)::bigint AS max_ms,
    COALESCE(AVG(
        CASE WHEN penalty = 'plus_two' THEN duration_ms + 2000 ELSE duration_ms END
    ) FILTER (WHERE penalty != 'dnf'), 0)::float8 AS mean_ms,
    COALESCE(STDDEV_POP(
        CASE WHEN penalty = 'plus_two' THEN duration_ms + 2000 ELSE duration_ms END
    ) FILTER (WHERE penalty != 'dnf'), 0)::float8 AS stddev_ms,
    COALESCE(SUM(
        CASE WHEN penalty = 'plus_two' THEN duration_ms + 2000 ELSE duration_ms END
    ) FILTER (WHERE penalty != 'dnf'), 0)::bigint AS total_ms,
    COUNT(*) FILTER (WHERE penalty = 'dnf')::bigint AS dnf_count
FROM solves
WHERE user_id = $1
    AND deleted_at IS NULL
    AND (sqlc.arg(event)::text = '' OR event = sqlc.arg(event)::text);

-- name: UserSolveAoN :many
SELECT
    CASE WHEN penalty = 'plus_two' THEN duration_ms + 2000 ELSE duration_ms END AS effective_ms,
    penalty
FROM solves
WHERE user_id = $1
    AND deleted_at IS NULL
    AND (sqlc.arg(event)::text = '' OR event = sqlc.arg(event)::text)
ORDER BY solved_at DESC
LIMIT sqlc.arg(limit_val);
