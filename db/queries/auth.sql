-- name: CreateUser :one
INSERT INTO users (id, email)
VALUES ($1, $2)
RETURNING *;

-- name: CreateAdminUser :one
INSERT INTO users (id, email, email_verified_at, user_role)
VALUES ($1, $2, now(), 'admin')
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = lower($1);

-- name: SetUserEmailVerified :exec
UPDATE users
SET email_verified_at = COALESCE(email_verified_at, now()), updated_at = now()
WHERE id = $1;

-- name: CreatePasswordCredential :exec
INSERT INTO password_credentials (user_id, password_hash)
VALUES ($1, $2);

-- name: UpsertPasswordCredential :exec
INSERT INTO password_credentials (user_id, password_hash)
VALUES ($1, $2)
ON CONFLICT (user_id) DO UPDATE
SET password_hash = EXCLUDED.password_hash, updated_at = now();

-- name: GetPasswordCredentialByEmail :one
SELECT u.id, u.email, u.email_verified_at, u.user_role, p.password_hash
FROM users u
JOIN password_credentials p ON p.user_id = u.id
WHERE u.email = lower($1);

-- name: CreateIdentity :exec
INSERT INTO identities (id, user_id, provider, subject, email)
VALUES ($1, $2, $3, $4, $5);

-- name: GetUserByIdentity :one
SELECT u.*
FROM identities i
JOIN users u ON u.id = i.user_id
WHERE i.provider = $1 AND i.subject = $2;

-- name: CreateRefreshToken :exec
INSERT INTO refresh_tokens (id, family_id, user_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4, $5);

-- name: GetRefreshTokenForUpdate :one
SELECT * FROM refresh_tokens
WHERE token_hash = $1
FOR UPDATE;

-- name: MarkRefreshTokenUsed :exec
UPDATE refresh_tokens SET used_at = now() WHERE id = $1 AND used_at IS NULL;

-- name: RevokeRefreshToken :exec
UPDATE refresh_tokens SET revoked_at = now()
WHERE token_hash = $1 AND revoked_at IS NULL;

-- name: RevokeRefreshFamily :exec
UPDATE refresh_tokens SET revoked_at = now()
WHERE family_id = $1 AND revoked_at IS NULL;

-- name: RevokeAllUserRefreshTokens :exec
UPDATE refresh_tokens SET revoked_at = now()
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: CreateOneTimeToken :exec
INSERT INTO one_time_tokens (id, user_id, kind, token_hash, expires_at)
VALUES ($1, $2, $3, $4, $5);

-- name: GetOneTimeTokenForUpdate :one
SELECT * FROM one_time_tokens
WHERE token_hash = $1 AND kind = $2
FOR UPDATE;

-- name: MarkOneTimeTokenUsed :exec
UPDATE one_time_tokens SET used_at = now() WHERE id = $1 AND used_at IS NULL;

-- name: InvalidateUserOneTimeTokens :exec
UPDATE one_time_tokens SET used_at = now()
WHERE user_id = $1 AND kind = $2 AND used_at IS NULL;

-- name: GetPasswordCredentialByUserID :one
SELECT * FROM password_credentials WHERE user_id = $1;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = $1;
