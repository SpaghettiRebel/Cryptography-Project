package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cryptochat/server/internal/accounts"
	"cryptochat/server/internal/store"
)

// SC-001: из 100 одновременных регистраций одного логина успешна ровно одна.
func TestConcurrentRegistration(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	s, err := accounts.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	var wins, taken atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			login := "Racer"
			if i%2 == 0 {
				login = "racer" // другой регистр — тот же логин
			}
			_, err := s.Register(ctx, login, "password-1")
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, store.ErrLoginTaken):
				taken.Add(1)
			default:
				t.Errorf("регистрация: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 || taken.Load() != 99 {
		t.Fatalf("успешных %d, занято %d", wins.Load(), taken.Load())
	}
}

func TestSessions(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	u, err := db.CreateUser(ctx, "Sess", "sess", "$h")
	if err != nil {
		t.Fatal(err)
	}
	token := []byte(strings.Repeat("h", 32))
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := db.CreateSession(ctx, token, u.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := db.SessionUser(ctx, token, now)
	if err != nil || got.ID != u.ID || got.Login != "Sess" {
		t.Fatalf("%+v, %v", got, err)
	}
	if _, err := db.SessionUser(ctx, token, now.Add(time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("просроченная: %v", err)
	}
	if err := db.DeleteExpiredSessions(ctx, u.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SessionUser(ctx, token, now); err != nil {
		t.Fatalf("действующая сессия удалена: %v", err)
	}
	if err := db.DeleteExpiredSessions(ctx, u.ID, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SessionUser(ctx, token, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("просроченная не удалена: %v", err)
	}
	if err := db.DeleteSession(ctx, token); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("удаление отсутствующей: %v", err)
	}
}

func TestUnavailable(t *testing.T) {
	if testing.Short() {
		t.Skip("сетевая проверка")
	}
	ctx := context.Background()
	// Порт 1 заведомо не слушается: Open не подключается сразу, ошибка приходит при запросе.
	db, err := Open(ctx, "postgres://u:p@127.0.0.1:1/db?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	start := time.Now()
	if _, err := db.UserByLogin(ctx, "x"); !errors.Is(err, store.ErrUnavailable) {
		t.Fatalf("запрос: %v", err)
	}
	if err := db.Ping(ctx); !errors.Is(err, store.ErrUnavailable) {
		t.Fatalf("Ping: %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("ошибка заняла %v", d)
	}
	// Отмена вызывающим кодом не считается сбоем хранилища.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := db.UserByLogin(canceled, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("отмена: %v", err)
	}
}

func TestBadDSNDoesNotLeak(t *testing.T) {
	_, err := Open(context.Background(), "postgres://u:секретный%zz@host/db")
	if err == nil || strings.Contains(err.Error(), "секретный") {
		t.Fatalf("%v", err)
	}
}
