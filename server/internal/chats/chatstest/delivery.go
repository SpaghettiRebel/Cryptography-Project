package chatstest

import (
	"errors"
	"strings"
	"testing"

	"cryptochat/server/internal/store"
)

// drain отмечает все закрытые чаты, оставшиеся от других проверок этого хранилища.
func (e *env) drain() {
	e.t.Helper()
	for {
		list, err := e.b.PendingQueueCleanup(bg, 100)
		if err != nil {
			e.t.Fatal(err)
		}
		if len(list) == 0 {
			return
		}
		for _, c := range list {
			if err := e.b.MarkQueuesDeleted(bg, c.ID); err != nil {
				e.t.Fatal(err)
			}
		}
	}
}

func (e *env) pending(limit int) [][16]byte {
	e.t.Helper()
	list, err := e.b.PendingQueueCleanup(bg, limit)
	if err != nil {
		e.t.Fatal(err)
	}
	out := make([][16]byte, 0, len(list))
	for _, c := range list {
		out = append(out, c.ID)
	}
	return out
}

func (e *env) state(id [16]byte, u store.UserRef) store.DeliveryState {
	e.t.Helper()
	st, err := e.b.DeliveryState(bg, id, u.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func runDelivery(t *testing.T, e *env, section func(string, func())) {
	section("состояние доставки при всех сочетаниях", func() {
		a, c := e.contacts()
		id := e.create(a, c)
		check := func(u store.UserRef, want store.DeliveryState) {
			e.t.Helper()
			if got := e.state(id, u); got != want {
				e.t.Fatalf("%s: %+v, ожидалось %+v", u.Login, got, want)
			}
		}
		// Создатель подключён, собеседник нет, значений нет.
		check(a, store.DeliveryState{PeerID: c.ID, Connected: true})
		check(c, store.DeliveryState{PeerID: a.ID})
		e.connect(id, c)
		check(c, store.DeliveryState{PeerID: a.ID, Connected: true})
		// Одного значения мало.
		e.expectErr(e.b.PublishValue(bg, id, a.ID, Value(t)), nil)
		check(a, store.DeliveryState{PeerID: c.ID, Connected: true})
		check(c, store.DeliveryState{PeerID: a.ID, Connected: true})
		e.expectErr(e.b.PublishValue(bg, id, c.ID, Value(t)), nil)
		check(a, store.DeliveryState{PeerID: c.ID, Connected: true, Ready: true})
		check(c, store.DeliveryState{PeerID: a.ID, Connected: true, Ready: true})
		// Отключение не снимает готовность и видно только самому участнику.
		if _, err := e.b.SetConnected(bg, id, a.ID, false); err != nil {
			e.t.Fatal(err)
		}
		check(a, store.DeliveryState{PeerID: c.ID, Ready: true})
		check(c, store.DeliveryState{PeerID: a.ID, Connected: true, Ready: true})
		e.closeChat(id, c)
		check(a, store.DeliveryState{PeerID: c.ID, Closed: true})
		check(c, store.DeliveryState{PeerID: a.ID, Closed: true})
	})
	section("состояние доставки: посторонний и несуществующий чат", func() {
		a, c := e.contacts()
		id := e.create(a, c)
		stranger := e.user("Stranger")
		_, err := e.b.DeliveryState(bg, id, stranger.ID)
		e.expectErr(err, store.ErrChatNotFound)
		_, err = e.b.DeliveryState(bg, NewID(e.t), a.ID)
		e.expectErr(err, store.ErrChatNotFound)
	})
	section("закрытые чаты ждут удаления очередей", func() {
		e.drain()
		a, c := e.contacts()
		active := e.create(a, c)
		first, second, third := e.create(a, c), e.create(a, c), e.create(a, c)
		if got := e.pending(10); len(got) != 0 {
			e.t.Fatalf("активные чаты в списке: %d", len(got))
		}
		e.closeChat(third, a)
		e.closeChat(first, c)
		e.closeChat(second, a)
		got := e.pending(10)
		if len(got) != 3 || got[0] != third || got[1] != first || got[2] != second {
			e.t.Fatalf("порядок: %v", got)
		}
		if limited := e.pending(2); len(limited) != 2 || limited[0] != third || limited[1] != first {
			e.t.Fatalf("предел: %v", limited)
		}
		list, err := e.b.PendingQueueCleanup(bg, 1)
		if err != nil {
			e.t.Fatal(err)
		}
		members := []string{list[0].UserLow, list[0].UserHigh}
		if !(members[0] == a.ID && members[1] == c.ID) && !(members[0] == c.ID && members[1] == a.ID) {
			e.t.Fatalf("участники: %v", members)
		}

		e.expectErr(e.b.MarkQueuesDeleted(bg, third), nil)
		e.expectErr(e.b.MarkQueuesDeleted(bg, third), nil)
		if got := e.pending(10); len(got) != 2 || got[0] != first {
			e.t.Fatalf("после отметки: %v", got)
		}
		// Повторное закрытие отметку назад не возвращает.
		e.closeChat(third, a)
		if got := e.pending(10); len(got) != 2 {
			e.t.Fatalf("после повторного закрытия: %v", got)
		}
		// Активный чат отметка не меняет.
		e.expectErr(e.b.MarkQueuesDeleted(bg, active), nil)
		e.closeChat(active, a)
		if got := e.pending(10); len(got) != 3 || got[2] != active {
			e.t.Fatalf("отметка задела активный чат: %v", got)
		}
		// Неизвестный чат: отметка не ошибка, поскольку идемпотентна.
		e.expectErr(e.b.MarkQueuesDeleted(bg, NewID(e.t)), nil)
		// Снятие отметки возвращает закрытый чат в список; повтор и неизвестный чат ничего не меняют.
		e.expectErr(e.b.ReopenQueueCleanup(bg, third), nil)
		e.expectErr(e.b.ReopenQueueCleanup(bg, third), nil)
		e.expectErr(e.b.ReopenQueueCleanup(bg, NewID(e.t)), nil)
		if got := e.pending(10); len(got) != 4 || got[0] != third {
			e.t.Fatalf("после снятия отметки: %v", got)
		}
	})
	section("удаление контакта ставит чаты в очередь на удаление", func() {
		e.drain()
		a, c := e.contacts()
		one, two := e.create(a, c), e.create(a, c)
		if err := e.b.RemoveContact(bg, a.ID, strings.ToLower(c.Login)); err != nil {
			e.t.Fatal(err)
		}
		got := e.pending(10)
		if len(got) != 2 || (got[0] != one && got[1] != one) || (got[0] != two && got[1] != two) {
			e.t.Fatalf("список: %v", got)
		}
		st := e.state(one, a)
		if !st.Closed || st.Connected {
			e.t.Fatalf("состояние: %+v", st)
		}
		if err := e.b.MarkQueuesDeleted(bg, one); errors.Is(err, store.ErrChatNotFound) {
			e.t.Fatal(err)
		}
	})
}
