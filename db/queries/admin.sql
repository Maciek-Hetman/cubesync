-- name: RecordRequestStat :exec
INSERT INTO request_stats_hourly (
    bucket_hour, method, route, status_code, request_count, total_duration_ms, max_duration_ms
) VALUES (
    $1, $2, $3, $4, 1, $5, $5
)
ON CONFLICT (bucket_hour, method, route, status_code) DO UPDATE
SET request_count = request_stats_hourly.request_count + 1,
    total_duration_ms = request_stats_hourly.total_duration_ms + EXCLUDED.total_duration_ms,
    max_duration_ms = GREATEST(request_stats_hourly.max_duration_ms, EXCLUDED.max_duration_ms);

-- name: GetOverviewStats :one
SELECT
    (SELECT COUNT(*)::bigint FROM users) AS total_users,
    (SELECT COUNT(*)::bigint FROM users WHERE email_verified_at IS NOT NULL) AS verified_users,
    (SELECT COUNT(*)::bigint FROM users WHERE created_at >= now() - interval '24 hours') AS new_users_24h,
    (SELECT COUNT(*)::bigint FROM users WHERE created_at >= now() - interval '7 days') AS new_users_7d,
    (SELECT COUNT(*)::bigint FROM users WHERE created_at >= now() - interval '30 days') AS new_users_30d,
    (SELECT COUNT(DISTINCT user_id)::bigint FROM devices WHERE last_seen_at >= now() - interval '24 hours') AS active_users_24h,
    (SELECT COUNT(DISTINCT user_id)::bigint FROM devices WHERE last_seen_at >= now() - interval '7 days') AS active_users_7d,
    (SELECT COUNT(DISTINCT user_id)::bigint FROM devices WHERE last_seen_at >= now() - interval '30 days') AS active_users_30d,
    (SELECT COUNT(*)::bigint FROM devices) AS total_devices,
    (SELECT COUNT(*)::bigint FROM cube_sessions) AS total_sessions,
    (SELECT COUNT(*)::bigint FROM solves) AS total_solves;

-- name: ListRequestStats :many
SELECT
    CASE
        WHEN sqlc.arg(interval)::text = 'day' THEN date_trunc('day', bucket_hour)
        ELSE bucket_hour
    END::timestamptz AS bucket,
    COALESCE(SUM(request_count), 0)::bigint AS request_count,
    COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 300 THEN request_count ELSE 0 END), 0)::bigint AS status_2xx,
    COALESCE(SUM(CASE WHEN status_code >= 300 AND status_code < 400 THEN request_count ELSE 0 END), 0)::bigint AS status_3xx,
    COALESCE(SUM(CASE WHEN status_code >= 400 AND status_code < 500 THEN request_count ELSE 0 END), 0)::bigint AS status_4xx,
    COALESCE(SUM(CASE WHEN status_code >= 500 THEN request_count ELSE 0 END), 0)::bigint AS status_5xx,
    COALESCE(SUM(total_duration_ms), 0)::bigint AS total_duration_ms,
    COALESCE(MAX(max_duration_ms), 0)::bigint AS max_duration_ms
FROM request_stats_hourly
WHERE bucket_hour >= sqlc.arg(from_time) AND bucket_hour < sqlc.arg(to_time)
GROUP BY 1
ORDER BY 1;

-- name: ListErrorStats :many
SELECT
    CASE
        WHEN sqlc.arg(interval)::text = 'day' THEN date_trunc('day', bucket_hour)
        ELSE bucket_hour
    END::timestamptz AS bucket,
    method,
    route,
    status_code,
    COALESCE(SUM(request_count), 0)::bigint AS request_count
FROM request_stats_hourly
WHERE bucket_hour >= sqlc.arg(from_time)
  AND bucket_hour < sqlc.arg(to_time)
  AND status_code >= 400
GROUP BY 1, method, route, status_code
ORDER BY 1, request_count DESC, method, route, status_code;
