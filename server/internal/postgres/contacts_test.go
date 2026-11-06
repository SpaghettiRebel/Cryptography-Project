package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"cryptochat/server/internal/contacts"
	"cryptochat/server/internal/contacts/contactstest"
	"cryptochat/server/internal/store"
)

func migrated(t *testing.T) *DB {
	t.Helper()
	db := testDB(t)
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestContactTransitions(t *testing.T) { contactstest.RunTransitions(t, migrated(t)) }

// SC-004: одновременные операции над одной парой не дают дублей и взаимных блокировок.
func TestConcurrentPairOperations(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	a, err := db.CreateUser(ctx, "PairA", "paira", "$h")
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.CreateUser(ctx, "PairB", "pairb", "$h")
	if err != nil {
		t.Fatal(err)
	}
	s := contacts.New(db)
	allowed := []error{store.ErrAlreadyContact, store.ErrDuplicateRequest, store.ErrRequestNotFound, store.ErrContactNotFound}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			switch i % 6 {
			case 0:
				_, err = s.SendRequest(ctx, a.ID, "PairB")
			case 1:
				_, err = s.SendRequest(ctx, b.ID, "PairA")
			case 2:
				err = s.AcceptRequest(ctx, b.ID, "PairA")
			case 3:
				err = s.AcceptRequest(ctx, a.ID, "PairB")
			case 4:
				err = s.RejectRequest(ctx, b.ID, "PairA")
			case 5:
				err = s.RemoveContact(ctx, a.ID, "PairB")
			}
			for _, ok := range allowed {
				if errors.Is(err, ok) {
					return
				}
			}
			if err != nil {
				t.Errorf("операция %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	var contactRows, requestRows int
	if err := db.pool.QueryRow(ctx, "SELECT count(*) FROM contacts").Scan(&contactRows); err != nil {
		t.Fatal(err)
	}
	if err := db.pool.QueryRow(ctx, "SELECT count(*) FROM contact_requests").Scan(&requestRows); err != nil {
		t.Fatal(err)
	}
	// Итог должен быть одним из согласованных состояний: контакт без запросов, либо запрос без контакта.
	if contactRows > 1 || requestRows > 1 || contactRows+requestRows > 1 {
		t.Fatalf("несогласованное состояние: контактов %d, запросов %d", contactRows, requestRows)
	}
}

// Встречные запросы, отправленные одновременно, всегда дают ровно один контакт.
func TestConcurrentCrossRequests(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	s := contacts.New(db)
	for i := 0; i < 20; i++ {
		a, err := db.CreateUser(ctx, fmt.Sprintf("CrossA%d", i), fmt.Sprintf("crossa%d", i), "$h")
		if err != nil {
			t.Fatal(err)
		}
		b, err := db.CreateUser(ctx, fmt.Sprintf("CrossB%d", i), fmt.Sprintf("crossb%d", i), "$h")
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		var becameA, becameB bool
		go func() { defer wg.Done(); becameA, _ = s.SendRequest(ctx, a.ID, b.Login) }()
		go func() { defer wg.Done(); becameB, _ = s.SendRequest(ctx, b.ID, a.Login) }()
		wg.Wait()
		if becameA == becameB {
			t.Fatalf("ровно один запрос должен создать контакт: %v, %v", becameA, becameB)
		}
		lists, err := s.List(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(lists.Contacts) != 1 || len(lists.Incoming)+len(lists.Outgoing) != 0 {
			t.Fatalf("списки: %+v", lists)
		}
	}
}
