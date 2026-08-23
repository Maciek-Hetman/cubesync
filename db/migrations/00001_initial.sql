-- +goose Up
CREATE TABLE users (
    id uuid PRIMARY KEY,
    email text NOT NULL,
    email_verified_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_email_normalized CHECK (email = lower(email))
);
CREATE UNIQUE INDEX users_email_unique ON users (lower(email));

CREATE TABLE password_credentials (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    password_hash text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE identities (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider text NOT NULL CHECK (provider IN ('google', 'apple')),
    subject text NOT NULL,
    email text,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, subject),
    UNIQUE (user_id, provider)
);

CREATE TABLE refresh_tokens (
    id uuid PRIMARY KEY,
    family_id uuid NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_user_id_idx ON refresh_tokens(user_id);
CREATE INDEX refresh_tokens_family_id_idx ON refresh_tokens(family_id);

CREATE TABLE one_time_tokens (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('verify_email', 'reset_password')),
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX one_time_tokens_user_kind_idx ON one_time_tokens(user_id, kind);

CREATE TABLE devices (
    id uuid NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name text NOT NULL DEFAULT '',
    platform text NOT NULL DEFAULT '',
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, id)
);

CREATE TABLE cube_sessions (
    id uuid NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name text NOT NULL DEFAULT '',
    event text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('manual', 'automatic')),
    started_at timestamptz NOT NULL,
    ended_at timestamptz,
    archived boolean NOT NULL DEFAULT false,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    PRIMARY KEY (user_id, id)
);

CREATE TABLE solves (
    id uuid NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id uuid,
    duration_ms bigint NOT NULL CHECK (duration_ms >= 0),
    penalty text NOT NULL CHECK (penalty IN ('none', 'plus_two', 'dnf')),
    solved_at timestamptz NOT NULL,
    scramble text NOT NULL DEFAULT '',
    event text NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    PRIMARY KEY (user_id, id),
    FOREIGN KEY (user_id, session_id) REFERENCES cube_sessions(user_id, id)
);
CREATE INDEX solves_user_solved_at_idx ON solves(user_id, solved_at DESC);
CREATE INDEX solves_user_session_idx ON solves(user_id, session_id);

CREATE TABLE processed_mutations (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_id uuid NOT NULL,
    mutation_id uuid NOT NULL,
    outcome jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, device_id, mutation_id),
    FOREIGN KEY (user_id, device_id) REFERENCES devices(user_id, id) ON DELETE CASCADE
);

CREATE TABLE change_log (
    change_id bigserial PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    entity_type text NOT NULL CHECK (entity_type IN ('session', 'solve')),
    entity_id uuid NOT NULL,
    operation text NOT NULL CHECK (operation IN ('upsert', 'delete')),
    version bigint NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX change_log_user_cursor_idx ON change_log(user_id, change_id);

-- +goose Down
DROP TABLE change_log;
DROP TABLE processed_mutations;
DROP TABLE solves;
DROP TABLE cube_sessions;
DROP TABLE devices;
DROP TABLE one_time_tokens;
DROP TABLE refresh_tokens;
DROP TABLE identities;
DROP TABLE password_credentials;
DROP TABLE users;
