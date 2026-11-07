package service

import (
	"sync/atomic"
	"testing"
	"time"

	"cryptochat/client/internal/apitest"
	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/localdb"
	"cryptochat/client/internal/profile"
	"cryptochat/client/internal/rpcclient"
	"cryptochat/core/cipher"
)

func TestOfflineAndRecovery(t *testing.T) {
	srv := apitest.NewServer()
	a := registered(t, srv, "alice")
	b := registered(t, srv, "bob")
	makeContacts(t, a, b)
	serverChat(t, a, "bob", rpcclient.ChatParams{Algorithm: cipher.RC6, KeyBits: 128, Mode: 2, Padding: 3})
	eventually(t, 2*time.Second, "чат", func() bool { return len(a.svc.Snapshot().Chats) == 1 })

	srv.SetDown(true)
	eventually(t, 2*time.Second, "offline", func() bool { return a.svc.Snapshot().Connection == Offline })
	s := a.svc.Snapshot()
	if s.Session == nil || len(s.Chats) != 1 || len(s.Contacts) != 1 {
		t.Fatalf("при обрыве потеряно локальное состояние: %+v", s)
	}
	if len(a.rec.notices(apperr.ServerUnavailable)) != 1 {
		t.Fatal("нет уведомления о потере связи")
	}
	if _, err := a.svc.SendContactRequest("carol"); apperr.CodeOf(err) != apperr.ServerUnavailable {
		t.Fatalf("действие без связи: %v", err)
	}
	n := a.api.Count("ListContacts")
	eventually(t, 2*time.Second, "цикл продолжает попытки", func() bool { return a.api.Count("ListContacts") >= n+3 })

	srv.SetDown(false)
	eventually(t, 2*time.Second, "online", func() bool { return a.svc.Snapshot().Connection == Online })
	if len(a.rec.notices("CONNECTION_RESTORED")) != 1 {
		t.Fatal("нет уведомления о восстановлении")
	}
	if a.svc.Snapshot().Session == nil || len(a.rec.ended()) != 0 {
		t.Fatal("сессия сброшена обрывом связи")
	}
}

func TestTimeoutMeansOffline(t *testing.T) {
	srv := apitest.NewServer()
	a := registered(t, srv, "alice")
	var slow atomic.Bool
	slow.Store(true)
	a.api.SetHook(func(m string) error {
		if slow.Load() {
			return apperr.New(apperr.Timeout)
		}
		return nil
	})
	eventually(t, 2*time.Second, "offline по сроку", func() bool { return a.svc.Snapshot().Connection == Offline })
	slow.Store(false)
	eventually(t, 2*time.Second, "online", func() bool { return a.svc.Snapshot().Connection == Online })
	if a.svc.Snapshot().Session == nil {
		t.Fatal("сессия сброшена")
	}
}

func TestSessionExpiredInBackground(t *testing.T) {
	srv := apitest.NewServer()
	a := registered(t, srv, "alice")
	srv.RevokeAll()
	eventually(t, 2*time.Second, "session-ended", func() bool { return len(a.rec.ended()) == 1 })
	e := a.rec.ended()[0]
	if e.Code != EndedExpired || e.Login != "alice" {
		t.Fatalf("%+v", e)
	}
	if a.svc.Snapshot().Session != nil {
		t.Fatal("сессия не сброшена")
	}
	// База профиля закрыта, данные на месте: повторный вход работает.
	eventually(t, 2*time.Second, "база закрыта", func() bool {
		db, err := localdb.Open(profile.DBPath(a.root, "localhost:8443", "id-alice"))
		if err != nil {
			return false
		}
		_ = db.Close()
		return true
	})
	if _, err := a.svc.Login("alice", testPassword); err != nil {
		t.Fatalf("повторный вход: %v", err)
	}
}

func TestSessionExpiredFromAction(t *testing.T) {
	srv := apitest.NewServer()
	a := registered(t, srv, "alice")
	a.api.SetHook(func(m string) error {
		if m == "SendContactRequest" {
			return apperr.New(apperr.SessionExpired)
		}
		return nil
	})
	if _, err := a.svc.SendContactRequest("bob"); apperr.CodeOf(err) != apperr.SessionExpired {
		t.Fatalf("%v", err)
	}
	if e := a.rec.ended(); len(e) != 1 || e[0].Code != EndedExpired {
		t.Fatalf("%+v", e)
	}
}

func TestServerBusyKeepsSession(t *testing.T) {
	srv := apitest.NewServer()
	a := registered(t, srv, "alice")
	var busy atomic.Bool
	busy.Store(true)
	a.api.SetHook(func(m string) error {
		if busy.Load() && m == "ListContacts" {
			return apperr.New(apperr.ServerBusy)
		}
		return nil
	})
	if _, err := a.svc.SendContactRequest("nobody"); apperr.CodeOf(err) != apperr.UserNotFound {
		t.Fatalf("%v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if s := a.svc.Snapshot(); s.Session == nil || s.Connection != Online || len(a.rec.ended()) != 0 {
		t.Fatalf("SERVER_BUSY сбросил сессию или связь: %+v", s)
	}
	busy.Store(false)
}

func TestPanicInSyncStepContinues(t *testing.T) {
	srv := apitest.NewServer()
	a := registered(t, srv, "alice")
	var once atomic.Bool
	a.api.SetHook(func(m string) error {
		if m == "ListChats" && once.CompareAndSwap(false, true) {
			panic("сбой шага синхронизации")
		}
		return nil
	})
	eventually(t, 2*time.Second, "уведомление INTERNAL", func() bool { return len(a.rec.notices(apperr.Internal)) == 1 })
	n := a.api.Count("ListChats")
	eventually(t, 2*time.Second, "следующий цикл", func() bool { return a.api.Count("ListChats") > n+1 })
	if a.svc.Snapshot().Session == nil {
		t.Fatal("сессия потеряна после паники")
	}
}

func TestLoginWhileServerDown(t *testing.T) {
	srv := apitest.NewServer()
	p := newParty(t, srv, t.TempDir())
	if err := p.svc.Register("alice", testPassword); err != nil {
		t.Fatal(err)
	}
	srv.SetDown(true)
	start := time.Now()
	if _, err := p.svc.Login("alice", testPassword); apperr.CodeOf(err) != apperr.ServerUnavailable {
		t.Fatalf("%v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("сообщение позже 5 с")
	}
	if p.svc.Snapshot().Connection != Offline {
		t.Fatal("индикатор связи не offline")
	}
}
