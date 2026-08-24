-- +goose Up
ALTER TABLE users
    ADD COLUMN user_role text NOT NULL DEFAULT 'user',
    ADD CONSTRAINT users_user_role_check CHECK (user_role IN ('user', 'admin'));

CREATE INDEX users_created_at_idx ON users (created_at);
CREATE INDEX devices_last_seen_at_idx ON devices (last_seen_at);

CREATE TABLE request_stats_hourly (
    bucket_hour timestamptz NOT NULL,
    method text NOT NULL,
    route text NOT NULL,
    status_code integer NOT NULL,
    request_count bigint NOT NULL DEFAULT 0 CHECK (request_count >= 0),
    total_duration_ms bigint NOT NULL DEFAULT 0 CHECK (total_duration_ms >= 0),
    max_duration_ms bigint NOT NULL DEFAULT 0 CHECK (max_duration_ms >= 0),
    PRIMARY KEY (bucket_hour, method, route, status_code)
);

-- +goose Down
DROP TABLE request_stats_hourly;
DROP INDEX devices_last_seen_at_idx;
DROP INDEX users_created_at_idx;
ALTER TABLE users DROP CONSTRAINT users_user_role_check;
ALTER TABLE users DROP COLUMN user_role;
