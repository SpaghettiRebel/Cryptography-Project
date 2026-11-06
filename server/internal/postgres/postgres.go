// Пакет postgres реализует хранилище сервера на Postgres.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"cryptochat/server/internal/store"
)

// queryTimeout ограничивает каждую операцию: при недоступной базе ответ приходит быстро (SC-008).
const queryTimeout = 3 * time.Second

// DB — подключение к базе.
type DB struct {
	pool *pgxpool.Pool
}

// Open создаёт пул. Подключения устанавливаются по мере необходимости, а после сбоя базы повторяются.
func Open(ctx context.Context, dsn string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// Текст ошибки разбора может содержать адрес с паролем, поэтому он не передаётся дальше.
		return nil, errors.New("postgres: некорректные параметры подключения")
	}
	cfg.ConnConfig.ConnectTimeout = queryTimeout
	cfg.MaxConnLifetime = 30 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: пул подключений: %w", err)
	}
	return &DB{pool: pool}, nil
}

// Close закрывает пул.
func (d *DB) Close() { d.pool.Close() }

// Ping проверяет готовность базы.
func (d *DB) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	if err := d.pool.Ping(ctx); err != nil {
		return fmt.Errorf("%w: %v", store.ErrUnavailable, err)
	}
	return nil
}

func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, queryTimeout)
}

// mapError оставляет отмену вызывающим кодом как есть, а сбои соединения и таймаут превращает в
// ErrUnavailable. Ошибки самого запроса, такие как нарушение ограничения, остаются ошибками запроса.
func mapError(parent context.Context, err error) error {
	if err == nil {
		return nil
	}
	if perr := parent.Err(); perr != nil {
		return perr
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && !serverUnavailable(pgErr.Code) {
		// В журнал идёт только код: текст ошибки базы может содержать значения из запроса.
		slog.Error("ошибка запроса к базе", "sqlstate", pgErr.Code)
		return fmt.Errorf("postgres: %s: %w", pgErr.Code, err)
	}
	return fmt.Errorf("%w: %v", store.ErrUnavailable, err)
}

// serverUnavailable отличает отказ сервера базы от ошибки запроса. При остановке Postgres присылает
// в открытые соединения 57P01, поэтому такой сбой приходит как PgError, а не как обрыв соединения.
func serverUnavailable(code string) bool {
	switch code {
	case "57P01", "57P02", "57P03": // остановка, сбой, запуск
		return true
	}
	return strings.HasPrefix(code, "08") || strings.HasPrefix(code, "53") // соединение, нехватка ресурсов
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
