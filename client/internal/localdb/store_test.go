package localdb

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/core/cipher"
)

func tempPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "профиль с пробелом", "client.db")
}

func open(t *testing.T, path string) *DB {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func chat(id byte, bits int) Chat {
	return Chat{ID: [16]byte{id}, Peer: "bob", Creator: "alice", Algorithm: cipher.RC6, KeyBits: bits,
		Mode: 2, Padding: 3, CreatedAt: time.UnixMilli(1_700_000_000_000)}
}

func bytesOf(b byte, n int) []byte { return bytes.Repeat([]byte{b}, n) }

func TestOpenPragmasAndMigrations(t *testing.T) {
	path := tempPath(t)
	db := open(t, path)
	for pragma, want := range map[string]string{"journal_mode": "wal", "synchronous": "2", "foreign_keys": "1", "locking_mode": "exclusive", "secure_delete": "1"} {
		var got string
		if err := db.sql.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil || got != want {
			t.Errorf("%s = %q (%v), ожидалось %q", pragma, got, err, want)
		}
	}
	var n int
	if err := db.sql.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("миграций %d: %v", n, err)
	}
	if db.Dir() != filepath.Dir(path) {
		t.Fatalf("Dir %q", db.Dir())
	}
	_ = db.Close()
	db2 := open(t, path)
	if err := db2.sql.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("повторное открытие изменило миграции: %d %v", n, err)
	}
}

func TestSecondCopyIsBusy(t *testing.T) {
	path := tempPath(t)
	first := open(t, path)
	if err := first.EnsureProfile("localhost:8443", "u1", "alice"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := Open(path)
	if apperr.CodeOf(err) != apperr.ProfileBusy {
		t.Fatalf("вторая копия: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("ожидание блокировки %v", d)
	}
	// Данные первой копии целы.
	if err := first.EnsureProfile("localhost:8443", "u1", "alice"); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	again := open(t, path)
	if err := again.EnsureProfile("localhost:8443", "u1", "alice"); err != nil {
		t.Fatal(err)
	}
}

func TestGarbageFileIsStorageError(t *testing.T) {
	path := tempPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("мусор вместо базы "), 500), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(path)
	e, ok := apperr.As(err)
	if !ok || e.Code != apperr.ProfileStorage || !strings.Contains(e.Message, filepath.Dir(path)) {
		t.Fatalf("%v", err)
	}
}

func TestProfileMismatch(t *testing.T) {
	db := open(t, tempPath(t))
	if err := db.EnsureProfile("localhost:8443", "u1", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureProfile("localhost:8443", "u1", "ALICE"); err != nil {
		t.Fatalf("тот же пользователь в другом регистре: %v", err)
	}
	if apperr.CodeOf(db.EnsureProfile("localhost:8443", "u2", "alice")) != apperr.ProfileStorage {
		t.Fatal("чужой user_id принят")
	}
}

func TestChatsLifecycle(t *testing.T) {
	path := tempPath(t)
	db := open(t, path)
	a, b := chat(1, 256), chat(2, 128)
	b.CreatedAt = a.CreatedAt.Add(time.Minute)
	for _, c := range []Chat{a, b, a} {
		if err := db.UpsertChat(c); err != nil {
			t.Fatal(err)
		}
	}
	active, err := db.KnownActiveChats()
	if err != nil || len(active) != 2 {
		t.Fatalf("%d %v", len(active), err)
	}
	got, ok, err := db.Chat(a.ID)
	if err != nil || !ok || got.Peer != "bob" || got.Algorithm != cipher.RC6 || got.KeyBits != 256 || got.Mode != 2 ||
		got.Padding != 3 || !got.CreatedAt.Equal(a.CreatedAt) || !got.ClosedAt.IsZero() {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	if err := db.MarkChatClosed(a.ID); err != nil {
		t.Fatal(err)
	}
	active, _ = db.KnownActiveChats()
	if len(active) != 1 || active[0].ID != b.ID {
		t.Fatalf("%+v", active)
	}
	// Повторная сверка закрытого чата не делает его активным.
	if err := db.UpsertChat(a); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := db.Chat(a.ID); got.ClosedAt.IsZero() {
		t.Fatal("закрытый чат снова активен")
	}
}

func TestSecretToKey(t *testing.T) {
	path := tempPath(t)
	db := open(t, path)
	c := chat(3, 192)
	if err := db.SaveDHSecret(c.ID, bytesOf(1, 256), bytesOf(2, 256)); err == nil {
		t.Fatal("секрет без строки чата принят")
	}
	if err := db.UpsertChat(c); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveDHSecret(c.ID, bytesOf(1, 256), bytesOf(2, 256)); err != nil {
		t.Fatal(err)
	}
	s, ok, err := db.DHSecret(c.ID)
	if err != nil || !ok || s.Published || !bytes.Equal(s.Private, bytesOf(1, 256)) || !bytes.Equal(s.Public, bytesOf(2, 256)) {
		t.Fatalf("%v %v", ok, err)
	}
	if err := db.MarkPublished(c.ID); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := db.DHSecret(c.ID); !s.Published {
		t.Fatal("published_at не записан")
	}
	if err := db.SaveSessionKeyAndDropSecret(c.ID, bytesOf(9, 32)); err == nil {
		t.Fatal("ключ неверной длины принят")
	}
	if _, ok, _ := db.DHSecret(c.ID); !ok {
		t.Fatal("после отказа секрет должен остаться")
	}
	if err := db.SaveSessionKeyAndDropSecret(c.ID, bytesOf(9, 24)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.DHSecret(c.ID); ok {
		t.Fatal("секрет не удалён вместе с записью ключа")
	}
	_ = db.Close()

	// Ключ переживает переоткрытие.
	db = open(t, path)
	if ok, err := db.SessionKeyExists(c.ID); err != nil || !ok {
		t.Fatalf("ключ потерян: %v", err)
	}
	if k, ok, err := db.SessionKey(c.ID); err != nil || !ok || !bytes.Equal(k, bytesOf(9, 24)) {
		t.Fatal("ключ отличается")
	}
	if err := db.DeleteChatSecrets(c.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.SessionKeyExists(c.ID); ok {
		t.Fatal("ключ не удалён")
	}
	if _, ok, _ := db.Chat(c.ID); !ok {
		t.Fatal("строка чата должна остаться")
	}
}

// randomBytes возвращает случайные байты, которые не встретятся в файле базы случайно.
func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// assertNotOnDisk проверяет, что ни в одном файле папки базы (основной файл, -wal, -shm) нет значения.
func assertNotOnDisk(t *testing.T, path, what string, value []byte) {
	t.Helper()
	files, err := filepath.Glob(path + "*")
	if err != nil || len(files) == 0 {
		t.Fatalf("файлы базы не найдены: %v", err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, value) {
			t.Errorf("%s остался в файле %s", what, filepath.Base(f))
		}
	}
}

// После удаления секрета и ключа их байтов нет ни в основном файле, ни в журнале WAL — пока база
// открыта и после закрытия.
func TestDeletedSecretsLeaveNoBytes(t *testing.T) {
	cases := map[string]bool{"запись только в журнале": false, "запись уже в основном файле": true}
	for name, reopen := range cases {
		t.Run(name, func(t *testing.T) {
			path := tempPath(t)
			db := open(t, path)
			c, d := chat(10, 256), chat(11, 128)
			for _, ch := range []Chat{c, d} {
				if err := db.UpsertChat(ch); err != nil {
					t.Fatal(err)
				}
			}
			// c: секрет удаляется вместе с записью ключа, затем удаляется и ключ.
			// d: секрет удаляется при закрытии чата.
			privC, pubC, privD, pubD := randomBytes(t, 256), randomBytes(t, 256), randomBytes(t, 256), randomBytes(t, 256)
			key := randomBytes(t, 32)
			if err := db.SaveDHSecret(c.ID, privC, pubC); err != nil {
				t.Fatal(err)
			}
			if err := db.SaveDHSecret(d.ID, privD, pubD); err != nil {
				t.Fatal(err)
			}
			if reopen {
				// Закрытие переносит журнал в основной файл: удаление должно затереть и его.
				_ = db.Close()
				db = open(t, path)
			}
			if err := db.SaveSessionKeyAndDropSecret(c.ID, key); err != nil {
				t.Fatal(err)
			}
			assertNotOnDisk(t, path, "секрет после выработки ключа", privC)
			if reopen {
				_ = db.Close()
				db = open(t, path)
			}
			if err := db.DeleteChatSecrets(c.ID); err != nil {
				t.Fatal(err)
			}
			if err := db.DeleteChatSecrets(d.ID); err != nil {
				t.Fatal(err)
			}
			assertNotOnDisk(t, path, "ключ", key)
			assertNotOnDisk(t, path, "секрет закрытого чата", privD)
			_ = db.Close()
			for what, v := range map[string][]byte{"секрет": privC, "ключ": key, "секрет закрытого чата": privD} {
				assertNotOnDisk(t, path, what+" после закрытия", v)
			}
		})
	}
}

func TestConstraints(t *testing.T) {
	db := open(t, tempPath(t))
	bad := []Chat{chat(4, 100), chat(5, 128), chat(6, 128), chat(7, 128)}
	bad[1].Algorithm = 3
	bad[2].Mode = 8
	bad[3].Padding = 0
	for i, c := range bad {
		if err := db.UpsertChat(c); err == nil {
			t.Errorf("%d: недопустимый чат принят", i)
		}
	}
	c := chat(8, 128)
	if err := db.UpsertChat(c); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveDHSecret(c.ID, bytesOf(1, 255), bytesOf(2, 256)); err == nil {
		t.Fatal("секрет неверной длины принят")
	}
	if _, err := db.sql.Exec(`INSERT INTO chats(chat_id, peer_login, creator_login, algorithm, key_bits, cipher_mode, padding_mode, created_at, synced_at)
		VALUES (x'01', 'a', 'b', 1, 128, 1, 1, 0, 0)`); err == nil {
		t.Fatal("идентификатор не из 16 байт принят")
	}
}
