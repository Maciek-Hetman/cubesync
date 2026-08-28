-- +goose Up
-- +goose NO TRANSACTION
ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS last_ack_cursor bigint NOT NULL DEFAULT 0;

CREATE INDEX CONCURRENTLY IF NOT EXISTS devices_user_ack_cursor_idx
    ON devices (user_id, last_ack_cursor);

-- +goose Down
ALTER TABLE devices DROP COLUMN IF EXISTS last_ack_cursor;
DROP INDEX IF EXISTS devices_user_ack_cursor_idx;
