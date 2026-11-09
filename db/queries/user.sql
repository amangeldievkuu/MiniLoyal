-- name: CreateUser :one
INSERT INTO users (id, email, password_hash, display_name)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2, sessions_valid_from = now(), updated_at = now() WHERE id = $1;

-- name: InvalidateUserSessions :exec
UPDATE users SET sessions_valid_from = now(), updated_at = now() WHERE id = $1;

-- name: TouchUserLogin :exec
UPDATE users SET last_login_at = now() WHERE id = $1;

-- name: SetPendingTotp :execrows
UPDATE users SET totp_secret_enc = $2, updated_at = now()
WHERE id = $1 AND totp_enabled_at IS NULL;

-- name: EnableTotp :execrows
UPDATE users SET totp_enabled_at = now(), updated_at = now()
WHERE id = $1 AND totp_secret_enc IS NOT NULL AND totp_enabled_at IS NULL;

-- name: DisableTotp :exec
UPDATE users SET totp_secret_enc = NULL, totp_enabled_at = NULL, updated_at = now() WHERE id = $1;

-- name: DeleteRecoveryCodes :exec
DELETE FROM totp_recovery_codes WHERE user_id = $1;

-- name: InsertRecoveryCode :exec
INSERT INTO totp_recovery_codes (user_id, code_hash) VALUES ($1, $2);

-- name: UseRecoveryCode :execrows
UPDATE totp_recovery_codes SET used_at = now()
WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL;

-- name: CountUnusedRecoveryCodes :one
SELECT count(*) FROM totp_recovery_codes WHERE user_id = $1 AND used_at IS NULL;
