package service

import (
	"slices"
	"testing"
	"time"

	"cryptochat/client/internal/apitest"
	"cryptochat/client/internal/apperr"
)

// Каждое действие вызывает метод сервера и сразу синхронизирует снимок.
func TestContactActionsSyncImmediately(t *testing.T) {
	srv := apitest.NewServer()
	a := registered(t, srv, "alice")
	b := registered(t, srv, "bob")
	c := registered(t, srv, "carol")
	_ = c

	a.api.ResetCalls()
	became, err := a.svc.SendContactRequest("bob")
	if err != nil || became {
		t.Fatalf("%v %v", became, err)
	}
	calls := a.api.Calls()
	if i := slices.Index(calls, "SendContactRequest"); i < 0 || !slices.Contains(calls[i:], "ListContacts") {
		t.Fatalf("нет синхронизации после действия: %v", calls)
	}
	if s := a.svc.Snapshot(); !slices.Equal(s.Outgoing, []string{"bob"}) {
		t.Fatalf("исходящие: %v", s.Outgoing)
	}
	// Входящий запрос появляется у второго участника без его действий.
	eventually(t, 2*time.Second, "входящий у bob", func() bool { return slices.Equal(b.svc.Snapshot().Incoming, []string{"alice"}) })

	if err := b.svc.AcceptContact("alice"); err != nil {
		t.Fatal(err)
	}
	if s := b.svc.Snapshot(); !slices.Equal(s.Contacts, []string{"alice"}) || len(s.Incoming) != 0 {
		t.Fatalf("%+v", s)
	}
	eventually(t, 2*time.Second, "контакт у alice", func() bool { return slices.Equal(a.svc.Snapshot().Contacts, []string{"bob"}) })

	if _, err := a.svc.SendContactRequest("carol"); err != nil {
		t.Fatal(err)
	}
	if err := c.svc.RejectContact("alice"); err != nil {
		t.Fatal(err)
	}
	if s := c.svc.Snapshot(); len(s.Incoming) != 0 || len(s.Contacts) != 0 {
		t.Fatalf("после отклонения: %+v", s)
	}
	eventually(t, 2*time.Second, "исходящий исчез у alice", func() bool { return len(a.svc.Snapshot().Outgoing) == 0 })

	if err := a.svc.RemoveContact("bob"); err != nil {
		t.Fatal(err)
	}
	if s := a.svc.Snapshot(); len(s.Contacts) != 0 {
		t.Fatalf("контакт не удалён: %v", s.Contacts)
	}
	eventually(t, 2*time.Second, "контакт исчез у bob", func() bool { return len(b.svc.Snapshot().Contacts) == 0 })
}

// Встречный запрос сразу создаёт контакт и даёт уведомление.
func TestMutualRequestNotice(t *testing.T) {
	srv := apitest.NewServer()
	a := registered(t, srv, "alice")
	b := registered(t, srv, "bob")
	if _, err := a.svc.SendContactRequest("bob"); err != nil {
		t.Fatal(err)
	}
	became, err := b.svc.SendContactRequest("alice")
	if err != nil || !became {
		t.Fatalf("встречный запрос: %v %v", became, err)
	}
	if n := b.rec.notices("CONTACT_CREATED"); len(n) != 1 || n[0].Kind != NoticeInfo {
		t.Fatalf("уведомление: %+v", n)
	}
	if s := b.svc.Snapshot(); !slices.Equal(s.Contacts, []string{"alice"}) {
		t.Fatalf("%v", s.Contacts)
	}
}

func TestContactErrors(t *testing.T) {
	srv := apitest.NewServer()
	a := registered(t, srv, "alice")
	b := registered(t, srv, "bob")
	check := func(what string, err error, code string) {
		t.Helper()
		e, ok := apperr.As(err)
		if !ok || e.Code != code || e.Message == "" {
			t.Errorf("%s: %v, ожидалось %s", what, err, code)
		}
	}
	_, err := a.svc.SendContactRequest("nobody")
	check("неизвестный", err, apperr.UserNotFound)
	_, err = a.svc.SendContactRequest("ALICE")
	check("себе", err, apperr.SelfRequest)
	if _, err := a.svc.SendContactRequest("bob"); err != nil {
		t.Fatal(err)
	}
	_, err = a.svc.SendContactRequest("bob")
	check("повтор", err, apperr.DuplicateRequest)
	check("принять несуществующий", a.svc.AcceptContact("bob"), apperr.RequestNotFound)
	check("отклонить несуществующий", a.svc.RejectContact("bob"), apperr.RequestNotFound)
	if err := b.svc.AcceptContact("alice"); err != nil {
		t.Fatal(err)
	}
	_, err = a.svc.SendContactRequest("bob")
	check("уже контакт", err, apperr.AlreadyContact)
	if err := a.svc.RemoveContact("bob"); err != nil {
		t.Fatal(err)
	}
	check("удалённый контакт", a.svc.RemoveContact("bob"), apperr.ContactNotFound)
}

func TestSnapshotListsSorted(t *testing.T) {
	srv := apitest.NewServer()
	a := registered(t, srv, "alice")
	for _, l := range []string{"zed", "Bob", "mike"} {
		p := registered(t, srv, l)
		if _, err := p.svc.SendContactRequest("alice"); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, 2*time.Second, "входящие", func() bool { return len(a.svc.Snapshot().Incoming) == 3 })
	if got := a.svc.Snapshot().Incoming; !slices.Equal(got, []string{"Bob", "mike", "zed"}) {
		t.Fatalf("порядок: %v", got)
	}
	v := a.svc.Snapshot().Version
	if v == 0 {
		t.Fatal("версия снимка не растёт")
	}
}
