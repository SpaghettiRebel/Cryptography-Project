package postgres

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	version int
	sql     string
}

var migrations = mustLoadMigrations()

func mustLoadMigrations() []migration {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		panic(err)
	}
	out := make([]migration, 0, len(entries))
	for _, e := range entries {
		number, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			panic("postgres: имя миграции без номера: " + e.Name())
		}
		v, err := strconv.Atoi(number)
		if err != nil {
			panic("postgres: номер миграции: " + e.Name())
		}
		body, err := migrationFiles.ReadFile("migrations/" + e.Name())
		if err != nil {
			panic(err)
		}
		out = append(out, migration{version: v, sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out
}

// migrationLock — произвольный постоянный ключ pg_advisory_lock.
const migrationLock = 7312004

// Migrate применяет новые миграции. Блокировка делает параллельный запуск безопасным.
func (d *DB) Migrate(ctx context.Context) error {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("postgres: подключение для миграций: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLock); err != nil {
		return fmt.Errorf("postgres: блокировка миграций: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLock) }()

	if _, err := conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version int PRIMARY KEY)"); err != nil {
		return fmt.Errorf("postgres: таблица миграций: %w", err)
	}
	for _, m := range migrations {
		var applied bool
		err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", m.version).Scan(&applied)
		if err != nil {
			return fmt.Errorf("postgres: миграция %d: %w", m.version, err)
		}
		if applied {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("postgres: миграция %d: %w", m.version, err)
		}
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("postgres: миграция %d: %w", m.version, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", m.version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("postgres: миграция %d: %w", m.version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("postgres: миграция %d: %w", m.version, err)
		}
	}
	return nil
}
