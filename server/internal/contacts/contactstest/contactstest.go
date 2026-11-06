// Пакет contactstest содержит общую проверку переходов из data-model.md: она запускается и на хранилище
// в памяти, и на настоящем Postgres. В сервер не входит.
package contactstest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"cryptochat/server/internal/contacts"
	"cryptochat/server/internal/store"
)

// Backend — хранилище, поддерживающее обе роли.
type Backend interface {
	store.AccountStore
	store.ContactStore
}

var bg = context.Background()

type env struct {
	t   *testing.T
	s   *contacts.Service
	b   Backend
	seq int
}

func (e *env) user(login string) store.UserRef {
	e.t.Helper()
	u, err := e.b.CreateUser(bg, login, strings.ToLower(login), "$hash")
	if err != nil {
		e.t.Fatal(err)
	}
	return store.UserRef{ID: u.ID, Login: u.Login}
}

func (e *env) pair() (store.UserRef, store.UserRef) {
	e.t.Helper()
	e.seq++
	return e.user(fmt.Sprintf("Alice-%d", e.seq)), e.user(fmt.Sprintf("Bob-%d", e.seq))
}

func logins(list []store.UserRef) string {
	out := make([]string, 0, len(list))
	for _, u := range list {
		out = append(out, u.Login)
	}
	return strings.Join(out, ",")
}

func (e *env) expect(u store.UserRef, contactsWant, in, out string) {
	e.t.Helper()
	got, err := e.s.List(bg, u.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	if logins(got.Contacts) != contactsWant || logins(got.Incoming) != in || logins(got.Outgoing) != out {
		e.t.Fatalf("%s: контакты %q, входящие %q, исходящие %q; ожидалось %q, %q, %q",
			u.Login, logins(got.Contacts), logins(got.Incoming), logins(got.Outgoing), contactsWant, in, out)
	}
}

// RunTransitions проверяет все переходы и запреты.
func RunTransitions(t *testing.T, b Backend) {
	e := &env{t: t, s: contacts.New(b), b: b}

	t.Run("запрос и принятие", func(t *testing.T) {
		e.t = t
		a, c := e.pair()
		became, err := e.s.SendRequest(bg, a.ID, c.Login)
		if err != nil || became {
			t.Fatalf("%v, %v", became, err)
		}
		e.expect(a, "", "", c.Login)
		e.expect(c, "", a.Login, "")
		if err := e.s.AcceptRequest(bg, c.ID, a.Login); err != nil {
			t.Fatal(err)
		}
		e.expect(a, c.Login, "", "")
		e.expect(c, a.Login, "", "")
	})

	t.Run("отклонение и новый запрос", func(t *testing.T) {
		e.t = t
		a, c := e.pair()
		if _, err := e.s.SendRequest(bg, a.ID, c.Login); err != nil {
			t.Fatal(err)
		}
		if err := e.s.RejectRequest(bg, c.ID, a.Login); err != nil {
			t.Fatal(err)
		}
		e.expect(a, "", "", "")
		e.expect(c, "", "", "")
		if _, err := e.s.SendRequest(bg, a.ID, c.Login); err != nil {
			t.Fatalf("новый запрос: %v", err)
		}
	})

	t.Run("удаление у обоих и новый запрос", func(t *testing.T) {
		e.t = t
		a, c := e.pair()
		if _, err := e.s.SendRequest(bg, a.ID, c.Login); err != nil {
			t.Fatal(err)
		}
		if err := e.s.AcceptRequest(bg, c.ID, a.Login); err != nil {
			t.Fatal(err)
		}
		if err := e.s.RemoveContact(bg, c.ID, a.Login); err != nil {
			t.Fatal(err)
		}
		e.expect(a, "", "", "")
		e.expect(c, "", "", "")
		if err := e.s.RemoveContact(bg, a.ID, c.Login); !errors.Is(err, store.ErrContactNotFound) {
			t.Fatalf("повторное удаление: %v", err)
		}
		if _, err := e.s.SendRequest(bg, a.ID, c.Login); err != nil {
			t.Fatalf("новый запрос: %v", err)
		}
	})

	t.Run("ошибки запроса", func(t *testing.T) {
		e.t = t
		a, c := e.pair()
		if _, err := e.s.SendRequest(bg, a.ID, a.Login); !errors.Is(err, store.ErrSelfRequest) {
			t.Errorf("себе: %v", err)
		}
		if _, err := e.s.SendRequest(bg, a.ID, strings.ToUpper(a.Login)); !errors.Is(err, store.ErrSelfRequest) {
			t.Errorf("себе в другом регистре: %v", err)
		}
		if _, err := e.s.SendRequest(bg, a.ID, "nobody-here"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("нет логина: %v", err)
		}
		if _, err := e.s.SendRequest(bg, a.ID, "не логин"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("логин вне правил: %v", err)
		}
		if _, err := e.s.SendRequest(bg, a.ID, c.Login); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.SendRequest(bg, a.ID, c.Login); !errors.Is(err, store.ErrDuplicateRequest) {
			t.Errorf("повтор: %v", err)
		}
		if err := e.s.AcceptRequest(bg, c.ID, a.Login); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.SendRequest(bg, a.ID, c.Login); !errors.Is(err, store.ErrAlreadyContact) {
			t.Errorf("уже контакт: %v", err)
		}
	})

	t.Run("встречный запрос", func(t *testing.T) {
		e.t = t
		a, c := e.pair()
		if _, err := e.s.SendRequest(bg, a.ID, c.Login); err != nil {
			t.Fatal(err)
		}
		became, err := e.s.SendRequest(bg, c.ID, a.Login)
		if err != nil || !became {
			t.Fatalf("%v, %v", became, err)
		}
		e.expect(a, c.Login, "", "")
		e.expect(c, a.Login, "", "")
	})

	t.Run("посторонний и отправитель", func(t *testing.T) {
		e.t = t
		a, c := e.pair()
		x := e.user(fmt.Sprintf("Mallory-%d", e.seq))
		if _, err := e.s.SendRequest(bg, a.ID, c.Login); err != nil {
			t.Fatal(err)
		}
		for name, err := range map[string]error{
			"посторонний принимает": e.s.AcceptRequest(bg, x.ID, a.Login),
			"посторонний отклоняет": e.s.RejectRequest(bg, x.ID, a.Login),
			"отправитель принимает": e.s.AcceptRequest(bg, a.ID, c.Login),
			"нет логина":            e.s.AcceptRequest(bg, c.ID, "nobody-here"),
		} {
			if !errors.Is(err, store.ErrRequestNotFound) {
				t.Errorf("%s: %v", name, err)
			}
		}
		e.expect(c, "", a.Login, "")
		if err := e.s.AcceptRequest(bg, c.ID, a.Login); err != nil {
			t.Fatal(err)
		}
		if err := e.s.RemoveContact(bg, x.ID, a.Login); !errors.Is(err, store.ErrContactNotFound) {
			t.Errorf("посторонний удаляет: %v", err)
		}
		e.expect(a, c.Login, "", "")
	})

	t.Run("регистр логина", func(t *testing.T) {
		e.t = t
		a, c := e.pair()
		if _, err := e.s.SendRequest(bg, a.ID, strings.ToLower(c.Login)); err != nil {
			t.Fatal(err)
		}
		if err := e.s.AcceptRequest(bg, c.ID, strings.ToUpper(a.Login)); err != nil {
			t.Fatal(err)
		}
		e.expect(a, c.Login, "", "") // написание как при регистрации
	})
}
