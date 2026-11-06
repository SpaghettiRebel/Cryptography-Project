// Пакет chatstest содержит общую проверку правил из data-model.md: она запускается и на хранилище в
// памяти, и на настоящем Postgres. В сервер не входит.
package chatstest

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"testing"

	"cryptochat/server/internal/store"
)

// Backend объединяет роли, нужные для подготовки пользователей и контактов.
type Backend interface {
	store.AccountStore
	store.ContactStore
	store.ChatStore
}

var (
	bg     = context.Background()
	params = store.ChatParams{Algorithm: 2, KeyBits: 256, CipherMode: 2, PaddingMode: 3}
)

type env struct {
	t   *testing.T
	b   Backend
	seq int
}

// NewID возвращает случайный идентификатор чата.
func NewID(t *testing.T) [16]byte {
	t.Helper()
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	return id
}

// Value возвращает случайное значение допустимой длины.
func Value(t *testing.T) []byte {
	t.Helper()
	v := make([]byte, 256)
	if _, err := rand.Read(v); err != nil {
		t.Fatal(err)
	}
	return v
}

func (e *env) user(prefix string) store.UserRef {
	e.t.Helper()
	e.seq++
	login := fmt.Sprintf("%s-%d", prefix, e.seq)
	u, err := e.b.CreateUser(bg, login, strings.ToLower(login), "$hash")
	if err != nil {
		e.t.Fatal(err)
	}
	return store.UserRef{ID: u.ID, Login: u.Login}
}

func (e *env) befriend(a, b store.UserRef) {
	e.t.Helper()
	if _, err := e.b.SendRequest(bg, a.ID, strings.ToLower(b.Login)); err != nil {
		e.t.Fatal(err)
	}
	if err := e.b.AcceptRequest(bg, b.ID, strings.ToLower(a.Login)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) contacts() (store.UserRef, store.UserRef) {
	e.t.Helper()
	a, b := e.user("Alice"), e.user("Bob")
	e.befriend(a, b)
	return a, b
}

func (e *env) create(a, b store.UserRef) [16]byte {
	e.t.Helper()
	id := NewID(e.t)
	if _, err := e.b.CreateChat(bg, id, a.ID, strings.ToLower(b.Login), params); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) get(id [16]byte, u store.UserRef) store.Chat {
	e.t.Helper()
	c, err := e.b.GetChat(bg, id, u.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) connect(id [16]byte, u store.UserRef) {
	e.t.Helper()
	if _, err := e.b.SetConnected(bg, id, u.ID, true); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) closeChat(id [16]byte, u store.UserRef) {
	e.t.Helper()
	if _, err := e.b.CloseChat(bg, id, u.ID); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) expectErr(err, want error) {
	e.t.Helper()
	if !errors.Is(err, want) {
		e.t.Fatalf("ошибка %v, ожидалось %v", err, want)
	}
}

func (e *env) activeCount(u store.UserRef) int {
	e.t.Helper()
	list, err := e.b.ListActiveChats(bg, u.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return len(list)
}

// Run проверяет все правила жизненного цикла, подключения и обмена значениями.
func Run(t *testing.T, b Backend) {
	e := &env{t: t, b: b}
	section := func(name string, fn func()) {
		t.Run(name, func(t *testing.T) {
			e.t = t
			fn()
		})
	}

	// US1: создание и закрытие.
	section("начальное состояние", func() {
		a, c := e.contacts()
		id := NewID(t)
		chat, err := b.CreateChat(bg, id, a.ID, strings.ToLower(c.Login), params)
		if err != nil {
			e.t.Fatal(err)
		}
		if chat.ID != id || chat.Params != params || chat.Creator.Login != a.Login || chat.ClosedAt != nil || chat.CreatedAt.IsZero() {
			e.t.Fatalf("чат %+v", chat)
		}
		if !chat.Self.Connected || chat.Peer.Connected || chat.Self.HasPublicValue || chat.Peer.HasPublicValue {
			e.t.Fatalf("участники %+v, %+v", chat.Self, chat.Peer)
		}
		if chat.Self.User.Login != a.Login || chat.Peer.User.Login != c.Login {
			e.t.Fatalf("логины %+v, %+v", chat.Self, chat.Peer)
		}
		seen := e.get(id, c)
		if seen.Self.User.Login != c.Login || seen.Peer.User.Login != a.Login || seen.Self.Connected || !seen.Peer.Connected {
			e.t.Fatalf("у собеседника %+v, %+v", seen.Self, seen.Peer)
		}
		if seen.Creator.Login != a.Login || seen.Params != params {
			e.t.Fatalf("параметры у собеседника %+v", seen)
		}
	})
	section("создание разрешено только с контактом", func() {
		a, c := e.user("Alice"), e.user("Bob")
		_, err := b.CreateChat(bg, NewID(t), a.ID, strings.ToLower(c.Login), params)
		e.expectErr(err, store.ErrNotContact)
		_, err = b.CreateChat(bg, NewID(t), a.ID, "nobody-at-all", params)
		e.expectErr(err, store.ErrNotContact)
		_, err = b.CreateChat(bg, NewID(t), a.ID, strings.ToLower(a.Login), params)
		e.expectErr(err, store.ErrSelfChat)
		// FR-021: отказ не оставляет записей.
		if e.activeCount(a) != 0 || e.activeCount(c) != 0 {
			e.t.Fatal("отказ оставил чат")
		}
	})
	section("список только активных, новые первыми", func() {
		a, c := e.contacts()
		first, second, third := e.create(a, c), e.create(c, a), e.create(a, c)
		e.closeChat(second, c)
		list, err := b.ListActiveChats(bg, a.ID)
		if err != nil {
			e.t.Fatal(err)
		}
		if len(list) != 2 || list[0].ID != third || list[1].ID != first {
			e.t.Fatalf("список из %d", len(list))
		}
	})
	section("закрытие для обоих и его повтор", func() {
		a, c := e.contacts()
		id := e.create(a, c)
		e.connect(id, c)
		closed, err := b.CloseChat(bg, id, c.ID)
		if err != nil {
			e.t.Fatal(err)
		}
		if closed.ClosedAt == nil || closed.Self.Connected || closed.Peer.Connected {
			e.t.Fatalf("после закрытия %+v", closed)
		}
		if e.activeCount(a) != 0 || e.activeCount(c) != 0 {
			e.t.Fatal("закрытый чат в списке")
		}
		seen := e.get(id, a)
		if seen.ClosedAt == nil {
			e.t.Fatal("участник не видит закрытия")
		}
		again, err := b.CloseChat(bg, id, a.ID)
		if err != nil || again.ClosedAt == nil || !again.ClosedAt.Equal(*closed.ClosedAt) {
			e.t.Fatalf("повтор: %+v, %v", again, err)
		}
	})
	section("посторонний не отличается от несуществующего чата", func() {
		a, c := e.contacts()
		stranger := e.user("Eve")
		id := e.create(a, c)
		for _, target := range [][16]byte{id, NewID(t)} {
			_, err := b.GetChat(bg, target, stranger.ID)
			e.expectErr(err, store.ErrChatNotFound)
			_, err = b.SetConnected(bg, target, stranger.ID, true)
			e.expectErr(err, store.ErrChatNotFound)
			_, err = b.SetConnected(bg, target, stranger.ID, false)
			e.expectErr(err, store.ErrChatNotFound)
			_, err = b.CloseChat(bg, target, stranger.ID)
			e.expectErr(err, store.ErrChatNotFound)
			e.expectErr(b.PublishValue(bg, target, stranger.ID, Value(t)), store.ErrChatNotFound)
			_, err = b.PeerValue(bg, target, stranger.ID)
			e.expectErr(err, store.ErrChatNotFound)
		}
		if got := e.get(id, a); got.ClosedAt != nil {
			e.t.Fatal("посторонний закрыл чат")
		}
	})
	section("удаление контакта закрывает чаты пары", func() {
		a, c := e.contacts()
		other := e.user("Carol")
		e.befriend(a, other)
		one, two := e.create(a, c), e.create(c, a)
		kept := e.create(a, other)
		value := Value(t)
		e.expectErr(b.PublishValue(bg, one, a.ID, value), nil)
		if err := b.RemoveContact(bg, a.ID, strings.ToLower(c.Login)); err != nil {
			e.t.Fatal(err)
		}
		for _, id := range [][16]byte{one, two} {
			seen := e.get(id, c)
			if seen.ClosedAt == nil || seen.Self.Connected || seen.Peer.Connected || seen.Peer.HasPublicValue {
				e.t.Fatalf("чат пары не закрыт: %+v", seen)
			}
		}
		if e.get(kept, a).ClosedAt != nil {
			e.t.Fatal("закрыт чат другой пары")
		}
		// Повторно добавленный контакт не открывает старые чаты.
		e.befriend(c, a)
		if e.get(one, a).ClosedAt == nil {
			e.t.Fatal("старый чат снова активен")
		}
		_, err := b.SetConnected(bg, one, a.ID, true)
		e.expectErr(err, store.ErrChatClosed)
	})

	// US2: подключение.
	section("подключение и отключение видны обоим", func() {
		a, c := e.contacts()
		id := e.create(a, c)
		joined, err := b.SetConnected(bg, id, c.ID, true)
		if err != nil || !joined.Self.Connected || !joined.Peer.Connected {
			e.t.Fatalf("подключение: %+v, %v", joined, err)
		}
		if !e.get(id, a).Peer.Connected {
			e.t.Fatal("создатель не видит подключения")
		}
		left, err := b.SetConnected(bg, id, a.ID, false)
		if err != nil || left.Self.Connected || left.ClosedAt != nil {
			e.t.Fatalf("отключение: %+v, %v", left, err)
		}
		if e.get(id, c).Peer.Connected {
			e.t.Fatal("собеседник не видит отключения")
		}
		if e.activeCount(a) != 1 {
			e.t.Fatal("отключение убрало чат")
		}
	})
	section("повторы подключения идемпотентны", func() {
		a, c := e.contacts()
		id := e.create(a, c)
		for _, connected := range []bool{true, true, false, false, true} {
			got, err := b.SetConnected(bg, id, c.ID, connected)
			if err != nil || got.Self.Connected != connected {
				e.t.Fatalf("%v: %+v, %v", connected, got.Self, err)
			}
		}
	})
	section("закрытый чат", func() {
		a, c := e.contacts()
		id := e.create(a, c)
		e.closeChat(id, a)
		_, err := b.SetConnected(bg, id, c.ID, true)
		e.expectErr(err, store.ErrChatClosed)
		got, err := b.SetConnected(bg, id, c.ID, false)
		if err != nil || got.ClosedAt == nil || got.Self.Connected {
			e.t.Fatalf("отключение от закрытого: %+v, %v", got, err)
		}
	})

	// US3: распределение ключа.
	section("публикация и получение значения", func() {
		a, c := e.contacts()
		id := e.create(a, c)
		e.connect(id, c)
		v, err := b.PeerValue(bg, id, c.ID)
		if err != nil || v != nil {
			e.t.Fatalf("значения ещё нет: %x, %v", v, err)
		}
		fromA, fromC := Value(t), Value(t)
		e.expectErr(b.PublishValue(bg, id, a.ID, fromA), nil)
		got := e.get(id, a)
		if !got.Self.HasPublicValue || got.Peer.HasPublicValue {
			e.t.Fatalf("наличие значений: %+v", got)
		}
		v, err = b.PeerValue(bg, id, c.ID)
		if err != nil || !bytes.Equal(v, fromA) {
			e.t.Fatalf("значение создателя: %v", err)
		}
		v, err = b.PeerValue(bg, id, a.ID)
		if err != nil || v != nil {
			e.t.Fatalf("значения собеседника ещё нет: %v", err)
		}
		e.expectErr(b.PublishValue(bg, id, c.ID, fromC), nil)
		got = e.get(id, c)
		if !got.Self.HasPublicValue || !got.Peer.HasPublicValue {
			e.t.Fatalf("оба значения: %+v", got)
		}
		v, err = b.PeerValue(bg, id, a.ID)
		if err != nil || !bytes.Equal(v, fromC) {
			e.t.Fatalf("значение собеседника: %v", err)
		}
	})
	section("значение нельзя заменить", func() {
		a, c := e.contacts()
		id := e.create(a, c)
		e.connect(id, c)
		value := Value(t)
		e.expectErr(b.PublishValue(bg, id, a.ID, value), nil)
		e.expectErr(b.PublishValue(bg, id, a.ID, value), nil)
		e.expectErr(b.PublishValue(bg, id, a.ID, Value(t)), store.ErrValueConflict)
		v, err := b.PeerValue(bg, id, c.ID)
		if err != nil || !bytes.Equal(v, value) {
			e.t.Fatalf("значение изменилось: %v", err)
		}
	})
	section("публикация требует подключения и активного чата", func() {
		a, c := e.contacts()
		id := e.create(a, c)
		e.expectErr(b.PublishValue(bg, id, c.ID, Value(t)), store.ErrNotConnected)
		if e.get(id, a).Peer.HasPublicValue {
			e.t.Fatal("значение сохранено без подключения")
		}
		e.closeChat(id, a)
		e.expectErr(b.PublishValue(bg, id, a.ID, Value(t)), store.ErrChatClosed)
	})
	section("отключённый получает значение, а переподключение его сохраняет", func() {
		a, c := e.contacts()
		id := e.create(a, c)
		value := Value(t)
		e.expectErr(b.PublishValue(bg, id, a.ID, value), nil)
		v, err := b.PeerValue(bg, id, c.ID) // собеседник ещё не подключался
		if err != nil || !bytes.Equal(v, value) {
			e.t.Fatalf("отключённый участник: %v", err)
		}
		if _, err := b.SetConnected(bg, id, a.ID, false); err != nil {
			e.t.Fatal(err)
		}
		e.connect(id, a)
		if !e.get(id, a).Self.HasPublicValue {
			e.t.Fatal("значение потеряно при переподключении")
		}
		e.expectErr(b.PublishValue(bg, id, a.ID, value), nil)
	})
	section("закрытие удаляет значения", func() {
		a, c := e.contacts()
		id := e.create(a, c)
		e.connect(id, c)
		e.expectErr(b.PublishValue(bg, id, a.ID, Value(t)), nil)
		e.expectErr(b.PublishValue(bg, id, c.ID, Value(t)), nil)
		e.closeChat(id, c)
		got := e.get(id, a)
		if got.Self.HasPublicValue || got.Peer.HasPublicValue {
			e.t.Fatalf("значения после закрытия: %+v", got)
		}
		_, err := b.PeerValue(bg, id, a.ID)
		e.expectErr(err, store.ErrChatClosed)
	})
}
