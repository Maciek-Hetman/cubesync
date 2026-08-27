-- +goose Up
CREATE TABLE request_errors (
    id bigserial PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now(),
    user_id uuid,
    method text NOT NULL,
    route text NOT NULL,
    status_code integer NOT NULL,
    code text NOT NULL,
    message text NOT NULL
);

CREATE INDEX request_errors_created_at_idx ON request_errors (created_at DESC);

-- +goose Down
DROP TABLE request_errors;
