-- +goose Up
-- +goose NO TRANSACTION

-- Item 3: change_log retention support
CREATE INDEX CONCURRENTLY IF NOT EXISTS change_log_created_at_idx
    ON change_log (created_at);

-- Item 5: Partial indexes on refresh_tokens for fast revocation
CREATE INDEX CONCURRENTLY IF NOT EXISTS refresh_tokens_active_user_idx
    ON refresh_tokens (user_id)
    WHERE revoked_at IS NULL AND used_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS refresh_tokens_active_family_idx
    ON refresh_tokens (family_id)
    WHERE revoked_at IS NULL;

-- Item 6: processed_mutations retention support
CREATE INDEX CONCURRENTLY IF NOT EXISTS processed_mutations_created_at_idx
    ON processed_mutations (created_at);

-- Item 11: request_stats_hourly retention support
CREATE INDEX CONCURRENTLY IF NOT EXISTS request_stats_hourly_bucket_hour_idx
    ON request_stats_hourly (bucket_hour);

-- +goose Down
DROP INDEX IF EXISTS request_stats_hourly_bucket_hour_idx;
DROP INDEX IF EXISTS processed_mutations_created_at_idx;
DROP INDEX IF EXISTS refresh_tokens_active_family_idx;
DROP INDEX IF EXISTS refresh_tokens_active_user_idx;
DROP INDEX IF EXISTS change_log_created_at_idx;
