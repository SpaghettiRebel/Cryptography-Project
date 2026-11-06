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

// FR-024: миграция чатов применяется поверх базы из 003 и сохраняет данные.
func TestMigrateUpgrade(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, "CREATE TABLE schema_migrations (version int PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, migrations[0].sql); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", migrations[0].version); err != nil {
		t.Fatal(err)
	}
	a, err := db.CreateUser(ctx, "OldAlice", "oldalice", "$h")
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.CreateUser(ctx, "OldBob", "oldbob", "$h")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SendRequest(ctx, a.ID, "oldbob"); err != nil {
		t.Fatal(err)
	}
	if err := db.AcceptRequest(ctx, b.ID, "oldalice"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	lists, err := db.ListContacts(ctx, a.ID)
	if err != nil || len(lists.Contacts) != 1 || lists.Contacts[0].Login != "OldBob" {
		t.Fatalf("контакты после обновления: %+v, %v", lists, err)
	}
	for _, table := range []string{"chats", "chat_participants"} {
		if _, err := db.pool.Exec(ctx, "SELECT 1 FROM "+table+" LIMIT 1"); err != nil {
			t.Errorf("таблица %s: %v", table, err)
		}
	}
}
