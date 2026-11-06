package postgres

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"cryptochat/server/internal/chats/chatstest"
	"cryptochat/server/internal/store"
)

func TestChatRules(t *testing.T) { chatstest.Run(t, migrated(t)) }

var chatParams = store.ChatParams{Algorithm: 1, KeyBits: 192, CipherMode: 4, PaddingMode: 2}

// chatPair создаёт двух контактов с логинами alice и bob.
func chatPair(t *testing.T, db *DB) (store.User, store.User) {
	t.Helper()
	ctx := context.Background()
	a, err := db.CreateUser(ctx, "Alice", "alice", "$h")
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.CreateUser(ctx, "Bob", "bob", "$h")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SendRequest(ctx, a.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := db.AcceptRequest(ctx, b.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func count(t *testing.T, db *DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Активного чата между бывшими контактами не остаётся, в каком бы порядке ни прошли операции.
func TestCreateChatRacesWithRemoveContact(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	a, b := chatPair(t, db)
	for round := 0; round < 20; round++ {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := db.CreateChat(ctx, chatstest.NewID(t), a.ID, "bob", chatParams)
			if err != nil && !errors.Is(err, store.ErrNotContact) {
				t.Errorf("создание: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := db.RemoveContact(ctx, b.ID, "alice"); err != nil {
				t.Errorf("удаление контакта: %v", err)
			}
		}()
		wg.Wait()
		if n := count(t, db, "SELECT count(*) FROM chats WHERE closed_at IS NULL"); n != 0 {
			t.Fatalf("круг %d: активных чатов %d", round, n)
		}
		if _, err := db.SendRequest(ctx, a.ID, "bob"); err != nil {
			t.Fatal(err)
		}
		if err := db.AcceptRequest(ctx, b.ID, "alice"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConcurrentClose(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	a, b := chatPair(t, db)
	id := chatstest.NewID(t)
	if _, err := db.CreateChat(ctx, id, a.ID, "bob", chatParams); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	times := map[string]bool{}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		user := a.ID
		if i%2 == 0 {
			user = b.ID
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := db.CloseChat(ctx, id, user)
			if err != nil {
				t.Errorf("закрытие: %v", err)
				return
			}
			mu.Lock()
			times[c.ClosedAt.String()] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(times) != 1 {
		t.Fatalf("время закрытия менялось: %v", times)
	}
}

func TestConnectRacesWithClose(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	a, b := chatPair(t, db)
	for round := 0; round < 20; round++ {
		id := chatstest.NewID(t)
		if _, err := db.CreateChat(ctx, id, a.ID, "bob", chatParams); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := db.SetConnected(ctx, id, b.ID, true); err != nil && !errors.Is(err, store.ErrChatClosed) {
				t.Errorf("подключение: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := db.CloseChat(ctx, id, a.ID); err != nil {
				t.Errorf("закрытие: %v", err)
			}
		}()
		wg.Wait()
		c, err := db.GetChat(ctx, id, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		if c.ClosedAt == nil || c.Self.Connected || c.Peer.Connected {
			t.Fatalf("после гонки: %+v", c)
		}
	}
}

// SC-004: 100 одновременных операций над одним чатом оставляют его согласованным.
func TestConcurrentChatOperations(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	a, b := chatPair(t, db)
	id := chatstest.NewID(t)
	if _, err := db.CreateChat(ctx, id, a.ID, "bob", chatParams); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetConnected(ctx, id, b.ID, true); err != nil {
		t.Fatal(err)
	}
	var published atomic.Int32
	var mu sync.Mutex
	var winner []byte
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch i % 4 {
			case 0, 1:
				value := chatstest.Value(t)
				err := db.PublishValue(ctx, id, a.ID, value)
				switch {
				case err == nil:
					published.Add(1)
					mu.Lock()
					winner = value
					mu.Unlock()
				case !errors.Is(err, store.ErrValueConflict):
					t.Errorf("публикация: %v", err)
				}
			case 2:
				if _, err := db.SetConnected(ctx, id, b.ID, false); err != nil {
					t.Errorf("отключение: %v", err)
				}
			case 3:
				if _, err := db.SetConnected(ctx, id, b.ID, true); err != nil {
					t.Errorf("подключение: %v", err)
				}
			}
		}(i)
	}
	wg.Wait()
	if published.Load() != 1 {
		t.Fatalf("успешных публикаций %d", published.Load())
	}
	got, err := db.PeerValue(ctx, id, b.ID)
	if err != nil || !bytes.Equal(got, winner) {
		t.Fatalf("сохранено не значение победителя: %v", err)
	}

	// Закрытие вперемешку с публикациями: закрытый чат не становится активным и не содержит значений.
	var closing sync.WaitGroup
	for i := 0; i < 100; i++ {
		closing.Add(1)
		go func(i int) {
			defer closing.Done()
			if i == 50 {
				if _, err := db.CloseChat(ctx, id, a.ID); err != nil {
					t.Errorf("закрытие: %v", err)
				}
				return
			}
			err := db.PublishValue(ctx, id, b.ID, chatstest.Value(t))
			if err != nil && !errors.Is(err, store.ErrValueConflict) && !errors.Is(err, store.ErrChatClosed) && !errors.Is(err, store.ErrNotConnected) {
				t.Errorf("публикация: %v", err)
			}
		}(i)
	}
	closing.Wait()
	if n := count(t, db, "SELECT count(*) FROM chat_participants WHERE chat_id = $1 AND (public_value IS NOT NULL OR connected)", id[:]); n != 0 {
		t.Fatalf("после закрытия остались значения или подключения: %d", n)
	}
	if n := count(t, db, "SELECT count(*) FROM chats WHERE id = $1 AND closed_at IS NOT NULL", id[:]); n != 1 {
		t.Fatal("чат не закрыт")
	}
}

func TestCloseErasesValues(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	a, b := chatPair(t, db)
	id := chatstest.NewID(t)
	if _, err := db.CreateChat(ctx, id, a.ID, "bob", chatParams); err != nil {
		t.Fatal(err)
	}
	if err := db.PublishValue(ctx, id, a.ID, chatstest.Value(t)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, "SELECT count(*) FROM chat_participants WHERE public_value IS NOT NULL"); n != 1 {
		t.Fatalf("значений до закрытия: %d", n)
	}
	if err := db.RemoveContact(ctx, b.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, "SELECT count(*) FROM chat_participants WHERE public_value IS NOT NULL OR connected"); n != 0 {
		t.Fatalf("после удаления контакта: %d", n)
	}
}

// FR-021: сбой на середине создания откатывает всё целиком.
func TestCreateChatRollsBack(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	a, _ := chatPair(t, db)
	for _, stmt := range []string{
		`CREATE FUNCTION fail_participant() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'сбой вставки'; END $$ LANGUAGE plpgsql`,
		`CREATE TRIGGER fail_participant BEFORE INSERT ON chat_participants FOR EACH ROW EXECUTE FUNCTION fail_participant()`,
	} {
		if _, err := db.pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	_, err := db.CreateChat(ctx, chatstest.NewID(t), a.ID, "bob", chatParams)
	if err == nil || errors.Is(err, store.ErrUnavailable) {
		t.Fatalf("ошибка: %v", err)
	}
	if n := count(t, db, "SELECT count(*) FROM chats"); n != 0 {
		t.Fatalf("чат не откатился: %d", n)
	}
}

func TestChatStoreUnavailable(t *testing.T) {
	if testing.Short() {
		t.Skip("сетевая проверка")
	}
	ctx := context.Background()
	db, err := Open(ctx, "postgres://u:p@127.0.0.1:1/db?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id := [16]byte{1}
	const user = "00000000-0000-0000-0000-000000000001"
	checks := map[string]error{}
	_, checks["CreateChat"] = db.CreateChat(ctx, id, user, "bob", chatParams)
	_, checks["ListActiveChats"] = db.ListActiveChats(ctx, user)
	_, checks["GetChat"] = db.GetChat(ctx, id, user)
	_, checks["SetConnected"] = db.SetConnected(ctx, id, user, true)
	_, checks["CloseChat"] = db.CloseChat(ctx, id, user)
	checks["PublishValue"] = db.PublishValue(ctx, id, user, make([]byte, 256))
	_, checks["PeerValue"] = db.PeerValue(ctx, id, user)
	for name, err := range checks {
		if !errors.Is(err, store.ErrUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRemoveContactRacesWithChatOperations(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	a, b := chatPair(t, db)
	for round := 0; round < 20; round++ {
		id := chatstest.NewID(t)
		if _, err := db.CreateChat(ctx, id, a.ID, "bob", chatParams); err != nil {
			t.Fatal(err)
		}
		allowed := func(err error) bool {
			return err == nil || errors.Is(err, store.ErrChatClosed) || errors.Is(err, store.ErrNotConnected)
		}
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			if err := db.PublishValue(ctx, id, a.ID, chatstest.Value(t)); !allowed(err) {
				t.Errorf("публикация: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := db.SetConnected(ctx, id, b.ID, true); !allowed(err) {
				t.Errorf("подключение: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := db.RemoveContact(ctx, b.ID, "alice"); err != nil {
				t.Errorf("удаление контакта: %v", err)
			}
		}()
		wg.Wait()
		if n := count(t, db, "SELECT count(*) FROM chat_participants WHERE chat_id = $1 AND (public_value IS NOT NULL OR connected)", id[:]); n != 0 {
			t.Fatalf("круг %d: остались значения или подключения: %d", round, n)
		}
		if _, err := db.SendRequest(ctx, a.ID, "bob"); err != nil {
			t.Fatal(err)
		}
		if err := db.AcceptRequest(ctx, b.ID, "alice"); err != nil {
			t.Fatal(err)
		}
	}
}

// Обход закрытых чатов использует частичный индекс и на непустых данных.
func TestQueueCleanupUsesIndex(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	a, _ := chatPair(t, db)
	var closed [][16]byte
	for i := 0; i < 6; i++ {
		id := chatstest.NewID(t)
		if _, err := db.CreateChat(ctx, id, a.ID, "bob", chatParams); err != nil {
			t.Fatal(err)
		}
		closed = append(closed, id)
	}
	// Много активных чатов: на маленькой таблице планировщик верно предпочитает полный обход.
	if _, err := db.pool.Exec(ctx, `INSERT INTO chats (id, user_low, user_high, creator_id, algorithm, key_bits, cipher_mode, padding_mode)
		SELECT decode(md5(i::text), 'hex'), user_low, user_high, creator_id, algorithm, key_bits, cipher_mode, padding_mode
		FROM chats, generate_series(1, 20000) AS i WHERE id = $1`, closed[0][:]); err != nil {
		t.Fatal(err)
	}
	for _, id := range closed {
		if _, err := db.CloseChat(ctx, id, a.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.pool.Exec(ctx, "ANALYZE chats"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.pool.Query(ctx, `EXPLAIN SELECT id, user_low::text, user_high::text FROM chats
		WHERE closed_at IS NOT NULL AND queues_deleted_at IS NULL ORDER BY closed_at, id LIMIT 100`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	if !strings.Contains(plan.String(), "chats_queue_cleanup_idx") {
		t.Fatalf("индекс не используется:\n%s", plan.String())
	}
	list, err := db.PendingQueueCleanup(ctx, 100)
	if err != nil || len(list) != len(closed) {
		t.Fatalf("список: %d, %v", len(list), err)
	}
}

// Закрытие и удаление контакта одновременно с обходом не теряют чат: отметка ставится только тому, что обход видел.
func TestCleanupDoesNotLoseChats(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	a, _ := chatPair(t, db)
	const total = 30
	ids := make([][16]byte, total)
	for i := range ids {
		ids[i] = chatstest.NewID(t)
		if _, err := db.CreateChat(ctx, ids[i], a.ID, "bob", chatParams); err != nil {
			t.Fatal(err)
		}
	}
	marked := map[[16]byte]bool{}
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		for {
			list, err := db.PendingQueueCleanup(ctx, 7)
			if err != nil {
				done <- err
				return
			}
			for _, c := range list {
				if err := db.MarkQueuesDeleted(ctx, c.ID); err != nil {
					done <- err
					return
				}
				marked[c.ID] = true
			}
			select {
			case <-stop:
				if len(list) == 0 {
					done <- nil
					return
				}
			default:
			}
		}
	}()
	var wg sync.WaitGroup
	for _, id := range ids[:total/2] {
		wg.Add(1)
		go func(id [16]byte) {
			defer wg.Done()
			if _, err := db.CloseChat(ctx, id, a.ID); err != nil {
				t.Error(err)
			}
		}(id)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := db.RemoveContact(ctx, a.ID, "bob"); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if !marked[id] {
			t.Errorf("чат %x потерян обходом", id[:2])
		}
	}
	if n := count(t, db, "SELECT count(*) FROM chats WHERE closed_at IS NULL"); n != 0 {
		t.Fatalf("активных чатов: %d", n)
	}
}

// Удаление контакта с несколькими чатами пары ставит в список все чаты.
func TestRemoveContactListsAllChats(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	a, _ := chatPair(t, db)
	want := map[[16]byte]bool{}
	for i := 0; i < 5; i++ {
		id := chatstest.NewID(t)
		if _, err := db.CreateChat(ctx, id, a.ID, "bob", chatParams); err != nil {
			t.Fatal(err)
		}
		want[id] = true
	}
	if err := db.RemoveContact(ctx, a.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	list, err := db.PendingQueueCleanup(ctx, 100)
	if err != nil || len(list) != len(want) {
		t.Fatalf("список: %d, %v", len(list), err)
	}
	for _, c := range list {
		if !want[c.ID] {
			t.Errorf("лишний чат %x", c.ID[:2])
		}
	}
}
