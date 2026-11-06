package accounts

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"cryptochat/server/internal/store"
	"cryptochat/server/internal/store/memstore"
)

var bg = context.Background()

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func setup(t *testing.T) (*Service, *memstore.Store, *clock) {
	t.Helper()
	m := memstore.New()
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s, err := newWithClock(bg, m, c.now)
	if err != nil {
		t.Fatal(err)
	}
	return s, m, c
}

func TestRegisterValidation(t *testing.T) {
	s, _, _ := setup(t)
	badLogins := []string{"", "ab", strings.Repeat("a", 33), "имя", "a b", "a/b", "abc\n"}
	for _, l := range badLogins {
		if _, err := s.Register(bg, l, "password-1"); !errors.Is(err, ErrInvalidLogin) {
			t.Errorf("логин %q: %v", l, err)
		}
	}
	for _, l := range []string{"abc", strings.Repeat("a", 32), "A_b-c.9"} {
		if _, err := s.Register(bg, l, "password-1"); err != nil {
			t.Errorf("логин %q: %v", l, err)
		}
	}
	for _, p := range []string{"", "1234567", strings.Repeat("x", 129)} {
		if _, err := s.Register(bg, "user-x", p); !errors.Is(err, ErrInvalidPassword) {
			t.Errorf("пароль длины %d: %v", len(p), err)
		}
	}
	for i, p := range []string{"12345678", strings.Repeat("x", 128), "пароль с пробелами"} {
		login := "edge" + string(rune('a'+i))
		if _, err := s.Register(bg, login, p); err != nil {
			t.Errorf("пароль длины %d: %v", len(p), err)
		}
		if _, err := s.Login(bg, login, p); err != nil {
			t.Errorf("вход с паролем длины %d: %v", len(p), err)
		}
	}
}

func TestLoginTakenKeepsRecord(t *testing.T) {
	s, m, _ := setup(t)
	u, err := s.Register(bg, "Alice", "password-1")
	if err != nil {
		t.Fatal(err)
	}
	if u.Login != "Alice" {
		t.Fatalf("написание логина: %s", u.Login)
	}
	before, _ := m.UserByLogin(bg, "alice")
	for _, l := range []string{"Alice", "alice", "ALICE"} {
		if _, err := s.Register(bg, l, "other-password"); !errors.Is(err, store.ErrLoginTaken) {
			t.Errorf("%s: %v", l, err)
		}
	}
	after, _ := m.UserByLogin(bg, "alice")
	if before != after {
		t.Fatal("существующая запись изменилась")
	}
	if _, err := s.Login(bg, "aLiCe", "password-1"); err != nil {
		t.Fatalf("вход в другом регистре: %v", err)
	}
}

func TestHashesDifferAndNoPlainPassword(t *testing.T) {
	s, m, _ := setup(t)
	if _, err := s.Register(bg, "first", "same-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(bg, "second", "same-password"); err != nil {
		t.Fatal(err)
	}
	a, _ := m.UserByLogin(bg, "first")
	b, _ := m.UserByLogin(bg, "second")
	if a.Hash == b.Hash {
		t.Fatal("хеши одинакового пароля совпали")
	}
	if strings.Contains(a.Hash, "same-password") {
		t.Fatal("пароль в хранилище")
	}
}

func TestLoginErrorsAreEqual(t *testing.T) {
	s, _, _ := setup(t)
	if _, err := s.Register(bg, "alice", "password-1"); err != nil {
		t.Fatal(err)
	}
	cases := map[string][2]string{
		"неверный пароль":     {"alice", "wrong-password"},
		"неизвестный логин":   {"nobody", "password-1"},
		"логин вне правил":    {"не логин", "password-1"},
		"пароль слишком длин": {"alice", strings.Repeat("x", 129)},
		"пустой пароль":       {"alice", ""},
	}
	for name, c := range cases {
		if _, err := s.Login(bg, c[0], c[1]); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSessionLifecycle(t *testing.T) {
	s, m, c := setup(t)
	u, err := s.Register(bg, "alice", "password-1")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Login(bg, "alice", "password-1")
	if err != nil {
		t.Fatal(err)
	}
	if sess.ExpiresAt != c.t.Add(24*time.Hour) || sess.User != u {
		t.Fatalf("сессия: %+v", sess)
	}
	if len(sess.Token) < 40 {
		t.Fatalf("короткая сессия: %d", len(sess.Token))
	}
	if got, err := s.Authenticate(bg, sess.Token); err != nil || got != u {
		t.Fatalf("Authenticate: %+v, %v", got, err)
	}

	// В хранилище только SHA-256.
	sum := sha256.Sum256([]byte(sess.Token))
	if !m.HasSession(sum[:]) {
		t.Fatal("в хранилище нет SHA-256 сессии")
	}
	if m.HasSession([]byte(sess.Token)) {
		t.Fatal("в хранилище открытая сессия")
	}

	for _, bad := range []string{"", "мусор"} {
		if _, err := s.Authenticate(bg, bad); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%q: %v", bad, err)
		}
	}

	// Граница срока: за секунду до конца действует, в момент окончания уже нет.
	c.t = sess.ExpiresAt.Add(-time.Second)
	if _, err := s.Authenticate(bg, sess.Token); err != nil {
		t.Fatalf("до конца срока: %v", err)
	}
	c.t = sess.ExpiresAt
	if _, err := s.Authenticate(bg, sess.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("просроченная: %v", err)
	}

	// Вход удаляет просроченные сессии пользователя.
	if !m.HasSession(sum[:]) {
		t.Fatal("сессия исчезла раньше времени")
	}
	next, err := s.Login(bg, "alice", "password-1")
	if err != nil {
		t.Fatal(err)
	}
	if m.HasSession(sum[:]) {
		t.Fatal("просроченная сессия не удалена")
	}

	if err := s.Logout(bg, next.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(bg, next.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("после выхода: %v", err)
	}
	if err := s.Logout(bg, next.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("повторный выход: %v", err)
	}
}

// failingStore имитирует сбой хранилища.
type failingStore struct{ store.AccountStore }

func (failingStore) UserByLogin(context.Context, string) (store.User, error) {
	return store.User{}, store.ErrUnavailable
}

func TestStoreFailureIsNotInvalidCredentials(t *testing.T) {
	s, err := New(bg, failingStore{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(bg, "alice", "password-1"); !errors.Is(err, store.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
}

// SC-010: среднее время неудачного входа не должно раскрывать существование логина.
func TestLoginTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("замер времени")
	}
	s, _, _ := setup(t)
	if _, err := s.Register(bg, "alice", "password-1"); err != nil {
		t.Fatal(err)
	}
	measure := func(login string) time.Duration {
		const runs = 30
		start := time.Now()
		for i := 0; i < runs; i++ {
			if _, err := s.Login(bg, login, "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatal(err)
			}
		}
		return time.Since(start) / runs
	}
	// Первый проход прогревает, затем чередование снижает влияние фона машины.
	measure("alice")
	var known, unknown time.Duration
	for i := 0; i < 3; i++ {
		known += measure("alice")
		unknown += measure("nobody")
	}
	ratio := float64(known) / float64(unknown)
	t.Logf("логин есть %v, логина нет %v, отношение %.3f", known/3, unknown/3, ratio)
	if ratio < 0.8 || ratio > 1.2 {
		t.Fatalf("время различается: отношение %.3f", ratio)
	}
}
