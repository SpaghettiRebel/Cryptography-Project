package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// testDB подключается к базе из CHAT_TEST_POSTGRES_DSN в отдельной временной схеме.
// Без переменной тест пропускается, а при CHAT_REQUIRE_POSTGRES=1 падает: так SC-001 и SC-004 нельзя
// пропустить незаметно.
func testDB(t *testing.T) *DB {
	t.Helper()
	if testing.Short() {
		t.Skip("нужен Postgres")
	}
	dsn := os.Getenv("CHAT_TEST_POSTGRES_DSN")
	if dsn == "" {
		if os.Getenv("CHAT_REQUIRE_POSTGRES") == "1" {
			t.Fatal("CHAT_TEST_POSTGRES_DSN не задан, а CHAT_REQUIRE_POSTGRES=1")
		}
		t.Skip("CHAT_TEST_POSTGRES_DSN не задан")
	}
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	schema := "t_" + hex.EncodeToString(raw)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanCtx, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		if _, err := admin.Exec(cleanCtx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("удаление схемы: %v", err)
		}
		_ = admin.Close(cleanCtx)
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}
