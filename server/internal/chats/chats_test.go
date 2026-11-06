package chats

import (
	"context"
	"errors"
	"testing"

	"cryptochat/server/internal/store"
	"cryptochat/server/internal/store/memstore"
)

var bg = context.Background()

var validParams = store.ChatParams{Algorithm: 1, KeyBits: 128, CipherMode: 1, PaddingMode: 1}

func setup(t *testing.T) (*Service, store.User) {
	t.Helper()
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
	return New(m), a
}

func TestAllValidParamsAccepted(t *testing.T) {
	s, a := setup(t)
	count := 0
	for algorithm := 1; algorithm <= 2; algorithm++ {
		for _, bits := range []int{128, 192, 256} {
			for mode := 1; mode <= 7; mode++ {
				for padding := 1; padding <= 4; padding++ {
					p := store.ChatParams{Algorithm: algorithm, KeyBits: bits, CipherMode: mode, PaddingMode: padding}
					c, err := s.Create(bg, a.ID, "BOB", p)
					if err != nil {
						t.Fatalf("%+v: %v", p, err)
					}
					if c.Params != p {
						t.Fatalf("сохранено %+v, ожидалось %+v", c.Params, p)
					}
					count++
				}
			}
		}
	}
	if count != 2*3*7*4 {
		t.Fatalf("сочетаний %d", count)
	}
}

func TestInvalidParams(t *testing.T) {
	s, a := setup(t)
	cases := []struct {
		field string
		edit  func(*store.ChatParams)
	}{
		{"algorithm", func(p *store.ChatParams) { p.Algorithm = 0 }},
		{"algorithm", func(p *store.ChatParams) { p.Algorithm = 3 }},
		{"key_bits", func(p *store.ChatParams) { p.KeyBits = 0 }},
		{"key_bits", func(p *store.ChatParams) { p.KeyBits = 512 }},
		{"cipher_mode", func(p *store.ChatParams) { p.CipherMode = 0 }},
		{"cipher_mode", func(p *store.ChatParams) { p.CipherMode = 8 }},
		{"padding_mode", func(p *store.ChatParams) { p.PaddingMode = 0 }},
		{"padding_mode", func(p *store.ChatParams) { p.PaddingMode = 5 }},
	}
	for _, c := range cases {
		p := validParams
		c.edit(&p)
		_, err := s.Create(bg, a.ID, "bob", p)
		var param ParamError
		if !errors.As(err, &param) || param.Field != c.field {
			t.Errorf("%+v: %v, ожидалось поле %s", p, err, c.field)
		}
	}
	if list, err := s.List(bg, a.ID); err != nil || len(list) != 0 {
		t.Fatalf("неверные параметры оставили чат: %d, %v", len(list), err)
	}
}

func TestIDs(t *testing.T) {
	s, a := setup(t)
	first, err := s.Create(bg, a.ID, "bob", validParams)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Create(bg, a.ID, "bob", validParams)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.ID == [16]byte{} {
		t.Fatalf("идентификаторы %x и %x", first.ID, second.ID)
	}
	for _, length := range []int{0, 15, 17} {
		raw := make([]byte, length)
		if _, err := s.Get(bg, a.ID, raw); !errors.Is(err, ErrInvalidChatID) {
			t.Errorf("Get, %d байт: %v", length, err)
		}
		if _, err := s.Connect(bg, a.ID, raw); !errors.Is(err, ErrInvalidChatID) {
			t.Errorf("Connect, %d байт: %v", length, err)
		}
		if _, err := s.Disconnect(bg, a.ID, raw); !errors.Is(err, ErrInvalidChatID) {
			t.Errorf("Disconnect, %d байт: %v", length, err)
		}
		if _, err := s.Close(bg, a.ID, raw); !errors.Is(err, ErrInvalidChatID) {
			t.Errorf("Close, %d байт: %v", length, err)
		}
	}
}

func TestInvalidPeerLoginIsNotContact(t *testing.T) {
	s, a := setup(t)
	if _, err := s.Create(bg, a.ID, "?", validParams); !errors.Is(err, store.ErrNotContact) {
		t.Fatal(err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("нет случайности") }

func TestRandomFailureIsReturned(t *testing.T) {
	s, a := setup(t)
	s.random = failingReader{}
	if _, err := s.Create(bg, a.ID, "bob", validParams); err == nil {
		t.Fatal("ошибка источника случайности потеряна")
	}
}

func TestReady(t *testing.T) {
	c := store.Chat{}
	if Ready(c) {
		t.Error("нет значений")
	}
	c.Self.HasPublicValue, c.Peer.HasPublicValue = true, true
	if !Ready(c) {
		t.Error("оба значения")
	}
	now := c.CreatedAt
	c.ClosedAt = &now
	if Ready(c) {
		t.Error("закрытый чат")
	}
}
