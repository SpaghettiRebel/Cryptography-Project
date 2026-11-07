// Пакет localdb — база профиля клиента на SQLite без cgo.
// База открывается с исключительной блокировкой: вторая копия приложения с тем же профилем получает
// PROFILE_BUSY и не меняет данные.
package localdb

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"cryptochat/client/internal/apperr"
)

//go:embed migrations/*.sql
var migrations embed.FS

// DB — открытая база профиля. Одно соединение: блокировка принадлежит ему.
type DB struct {
	sql *sql.DB
	dir string
	now func() time.Time
}

// secure_delete затирает нулями содержимое удалённых строк: секрет и ключ закрытого чата не остаются
// в свободном месте страниц.
var pragmas = []string{
	"locking_mode(EXCLUSIVE)",
	"journal_mode(WAL)",
	"synchronous(FULL)",
	"foreign_keys(ON)",
	"busy_timeout(200)",
	"secure_delete(ON)",
}

// Open открывает или создаёт базу, применяет миграции и берёт блокировку до возврата.
func Open(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, apperr.Profile(dir)
	}
	q := url.Values{}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	conn, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+q.Encode())
	if err != nil {
		return nil, apperr.Profile(dir)
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	conn.SetConnMaxLifetime(0)
	conn.SetConnMaxIdleTime(0)
	db := &DB{sql: conn, dir: dir, now: time.Now}
	if err := db.init(); err != nil {
		_ = conn.Close()
		if isBusy(err) {
			return nil, apperr.New(apperr.ProfileBusy)
		}
		return nil, apperr.Profile(dir)
	}
	_ = os.Chmod(path, 0o600)
	return db, nil
}

func (d *DB) init() error {
	ctx := context.Background()
	if err := d.migrate(ctx); err != nil {
		return err
	}
	// Запись в режиме EXCLUSIVE берёт блокировку и держит её до закрытия.
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM profile WHERE key = 'lock'`); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (d *DB) migrate(ctx context.Context) error {
	var exists int
	if err := d.sql.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&exists); err != nil {
		return err
	}
	applied := map[int]bool{}
	if exists == 1 {
		rows, err := d.sql.QueryContext(ctx, `SELECT version FROM schema_migrations`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v int
			if err := rows.Scan(&v); err != nil {
				_ = rows.Close()
				return err
			}
			applied[v] = true
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		base := filepath.Base(name)
		v, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("имя миграции %s", base)
		}
		if applied[v] {
			continue
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := d.sql.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
			v, d.now().UnixMilli()); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func isBusy(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		c := se.Code() & 0xff
		return c == sqlite3.SQLITE_BUSY || c == sqlite3.SQLITE_LOCKED
	}
	return false
}

// checkpoint переносит журнал WAL в основной файл и обрезает журнал до нуля: старые кадры журнала
// с удалённым секретом не остаются в файле -wal.
func (d *DB) checkpoint() error {
	var busy, frames, done int
	if err := d.sql.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &done); err != nil {
		return err
	}
	if busy != 0 {
		return fmt.Errorf("журнал WAL не перенесён: база занята")
	}
	return nil
}

// Dir — папка профиля.
func (d *DB) Dir() string { return d.dir }

// Close закрывает базу и снимает блокировку.
func (d *DB) Close() error { return d.sql.Close() }
