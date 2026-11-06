package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"cryptochat/server/internal/store"
)

// CreateUser создаёт пользователя; уникальность обеспечивает индекс по login_norm.
func (d *DB) CreateUser(parent context.Context, login, norm, hash string) (store.User, error) {
	ctx, cancel := withTimeout(parent)
	defer cancel()
	var id string
	err := d.pool.QueryRow(ctx,
		`INSERT INTO users (login, login_norm, password_hash) VALUES ($1, $2, $3) RETURNING id::text`,
		login, norm, hash).Scan(&id)
	if isUniqueViolation(err) {
		return store.User{}, store.ErrLoginTaken
	}
	if err != nil {
		return store.User{}, mapError(parent, err)
	}
	return store.User{ID: id, Login: login, Hash: hash}, nil
}

// UserByLogin ищет пользователя по нормализованному логину.
func (d *DB) UserByLogin(parent context.Context, norm string) (store.User, error) {
	ctx, cancel := withTimeout(parent)
	defer cancel()
	var u store.User
	err := d.pool.QueryRow(ctx, `SELECT id::text, login, password_hash FROM users WHERE login_norm = $1`, norm).
		Scan(&u.ID, &u.Login, &u.Hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.User{}, store.ErrNotFound
	}
	if err != nil {
		return store.User{}, mapError(parent, err)
	}
	return u, nil
}

// CreateSession сохраняет SHA-256 сессии.
func (d *DB) CreateSession(parent context.Context, tokenHash []byte, userID string, expiresAt time.Time) error {
	ctx, cancel := withTimeout(parent)
	defer cancel()
	_, err := d.pool.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2::uuid, $3)`,
		tokenHash, userID, expiresAt)
	return mapError(parent, err)
}

// SessionUser возвращает владельца действующей сессии.
func (d *DB) SessionUser(parent context.Context, tokenHash []byte, now time.Time) (store.UserRef, error) {
	ctx, cancel := withTimeout(parent)
	defer cancel()
	var u store.UserRef
	err := d.pool.QueryRow(ctx, `
		SELECT u.id::text, u.login FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > $2`, tokenHash, now).Scan(&u.ID, &u.Login)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.UserRef{}, store.ErrNotFound
	}
	if err != nil {
		return store.UserRef{}, mapError(parent, err)
	}
	return u, nil
}

// DeleteSession удаляет сессию.
func (d *DB) DeleteSession(parent context.Context, tokenHash []byte) error {
	ctx, cancel := withTimeout(parent)
	defer cancel()
	tag, err := d.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	if err != nil {
		return mapError(parent, err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// DeleteExpiredSessions удаляет просроченные сессии пользователя.
func (d *DB) DeleteExpiredSessions(parent context.Context, userID string, now time.Time) error {
	ctx, cancel := withTimeout(parent)
	defer cancel()
	_, err := d.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1::uuid AND expires_at <= $2`, userID, now)
	return mapError(parent, err)
}
