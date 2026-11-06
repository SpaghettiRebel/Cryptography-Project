package keyexchange

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"cryptochat/server/internal/chats"
	"cryptochat/server/internal/store"
	"cryptochat/server/internal/store/memstore"
)

var bg = context.Background()

func TestPublishChecksLengthFirst(t *testing.T) {
	s := New(memstore.New())
	unknown := make([]byte, 16)
	for _, length := range []int{0, 255, 257} {
		if err := s.Publish(bg, "кто-угодно", unknown, make([]byte, length)); !errors.Is(err, ErrInvalidPublicValue) {
			t.Errorf("%d байт: %v", length, err)
		}
	}
	if err := s.Publish(bg, "кто-угодно", unknown, make([]byte, PublicValueSize)); !errors.Is(err, store.ErrChatNotFound) {
		t.Errorf("значение верной длины: %v", err)
	}
	if err := s.Publish(bg, "кто-угодно", []byte{1}, make([]byte, PublicValueSize)); !errors.Is(err, chats.ErrInvalidChatID) {
		t.Errorf("идентификатор: %v", err)
	}
	if _, err := s.Peer(bg, "кто-угодно", []byte{1}); !errors.Is(err, chats.ErrInvalidChatID) {
		t.Errorf("идентификатор: %v", err)
	}
}

func TestValueIsCopied(t *testing.T) {
	m := memstore.New()
	a, err := m.CreateUser(bg, "Alice", "alice", "$h")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.CreateUser(bg, "Bob", "bob", "$h")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SendRequest(bg, a.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := m.AcceptRequest(bg, b.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	id := [16]byte{1, 2, 3}
	if _, err := m.CreateChat(bg, id, a.ID, "bob", store.ChatParams{Algorithm: 1, KeyBits: 128, CipherMode: 1, PaddingMode: 1}); err != nil {
		t.Fatal(err)
	}
	s := New(m)
	value := bytes.Repeat([]byte{7}, PublicValueSize)
	if err := s.Publish(bg, a.ID, id[:], value); err != nil {
		t.Fatal(err)
	}
	value[0] = 9
	got, err := s.Peer(bg, b.ID, id[:])
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 7 {
		t.Fatal("сохранённое значение зависит от входного среза")
	}
	got[1] = 9
	again, err := s.Peer(bg, b.ID, id[:])
	if err != nil || again[1] != 7 {
		t.Fatalf("сохранённое значение зависит от выданного среза: %v", err)
	}
}
