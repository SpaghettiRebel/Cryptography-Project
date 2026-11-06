package postgres

import (
	"context"
	"sync"
	"testing"
)

func TestMigrate(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("повторный запуск: %v", err)
	}
	var count int
	if err := db.pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(migrations) {
		t.Fatalf("применено %d миграций из %d", count, len(migrations))
	}
	for _, table := range []string{"users", "sessions", "contact_requests", "contacts"} {
		if _, err := db.pool.Exec(ctx, "SELECT 1 FROM "+table+" LIMIT 1"); err != nil {
			t.Errorf("таблица %s: %v", table, err)
		}
	}
	if err := db.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateConcurrent(t *testing.T) {
	db := testDB(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := db.Migrate(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
