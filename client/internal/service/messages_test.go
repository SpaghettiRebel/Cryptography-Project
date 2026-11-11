package service

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cryptochat/client/internal/apitest"
	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/delivery"
	"cryptochat/client/internal/keyagree"
	"cryptochat/client/internal/rpcclient"
	"cryptochat/core/cipher"
)

// readyChat — чат alice и bob с согласованным ключом и открытым приёмом у обоих.
func readyChat(t *testing.T, mode, pad int) (*apitest.Server, *party, *party, [16]byte, string) {
	t.Helper()
	srv, a, b := pairWithContacts(t)
	id := serverChat(t, a, "bob", rpcclient.ChatParams{Algorithm: cipher.RC6, KeyBits: 256, Mode: mode, Padding: pad})
	hexID := hex.EncodeToString(id[:])
	eventually(t, 3*time.Second, "чат у bob", func() bool { _, ok := findChat(b, id); return ok })
	if _, err := b.svc.ConnectChat(hexID); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "ключ и приём у обоих", func() bool {
		for _, p := range []*party{a, b} {
			c, ok := findChat(p, id)
			if !ok || c.KeyState != keyagree.Ready || c.ReceiveState != delivery.RecvReceiving {
				return false
			}
		}
		return true
	})
	return srv, a, b, id, hexID
}

func TestTextValidation(t *testing.T) {
	_, a, _, _, hexID := readyChat(t, 2, 3)
	if _, err := a.svc.SendText(hexID, " \n\t "); apperr.CodeOf(err) != apperr.EmptyText {
		t.Fatalf("пустой текст: %v", err)
	}
	if _, err := a.svc.SendText(hexID, strings.Repeat("a", delivery.MaxText+1)); apperr.CodeOf(err) != apperr.TextTooLong {
		t.Fatalf("65 537 байт: %v", err)
	}
	if _, err := a.svc.SendText("zz", "x"); apperr.CodeOf(err) != apperr.InvalidChatID {
		t.Fatalf("%v", err)
	}
	dir := t.TempDir()
	if _, err := a.svc.SendFile(hexID, filepath.Join(dir, "нет такого")); apperr.CodeOf(err) != apperr.FileRead {
		t.Fatalf("нечитаемый путь: %v", err)
	}
	big := filepath.Join(dir, "big.bin")
	f, _ := os.Create(big)
	_ = f.Truncate(delivery.MaxFile + 1)
	_ = f.Close()
	if _, err := a.svc.SendFile(hexID, big); apperr.CodeOf(err) != apperr.FileTooLarge {
		t.Fatalf("256 МиБ + 1: %v", err)
	}
	if n := a.api.Count("SendFragment"); n != 0 {
		t.Fatalf("SendFragment: %d", n)
	}
}

func TestClosedChatHistory(t *testing.T) {
	_, a, _, id, hexID := readyChat(t, 2, 3)
	if err := a.svc.DeleteChatHistory(hexID); apperr.CodeOf(err) != apperr.ChatActive {
		t.Fatalf("активный чат: %v", err)
	}
	// Сообщения создаются в обход проверок отправки: в сборке без тестового шифра они получат FAILED.
	for i := 0; i < 5; i++ {
		if _, err := a.svc.sess.engine.SendText(id, "x"); err != nil {
			t.Fatal(err)
		}
	}
	page, err := a.svc.ListMessages(hexID, 0, 0)
	if err != nil || len(page.Messages) != 1 || !page.HasMore {
		t.Fatalf("limit 0 → 1: %+v %v", page, err)
	}
	page, _ = a.svc.ListMessages(hexID, 0, 1000)
	if len(page.Messages) != 5 || page.HasMore {
		t.Fatalf("limit 1000 → 200: %d", len(page.Messages))
	}
	page2, _ := a.svc.ListMessages(hexID, page.Messages[2].Seq, 10)
	if len(page2.Messages) != 2 || page2.Messages[1].Seq != page.Messages[1].Seq {
		t.Fatal("beforeSeq")
	}
	if err := a.svc.CloseChat(hexID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.SendText(hexID, "после закрытия"); apperr.CodeOf(err) != apperr.SendBlocked {
		t.Fatalf("закрытый чат: %v", err)
	} else if e, _ := apperr.As(err); e.Field != apperr.BlockChatClosed {
		t.Fatalf("причина %q", e.Field)
	}
	closed, err := a.svc.ListClosedChats()
	if err != nil || len(closed) != 1 || closed[0].ID != id || closed[0].MessageCount != 5 {
		t.Fatalf("%+v %v", closed, err)
	}
	if page, err := a.svc.ListMessages(hexID, 0, 50); err != nil || len(page.Messages) != 5 {
		t.Fatal("история закрытого чата")
	}
	if err := a.svc.DeleteChatHistory(hexID); err != nil {
		t.Fatal(err)
	}
	if page, _ := a.svc.ListMessages(hexID, 0, 50); len(page.Messages) != 0 {
		t.Fatal("история не удалена")
	}
	if _, ok, _ := a.svc.sess.db.Chat(id); !ok {
		t.Fatal("строка чата удалена")
	}
	if a.rec.count(EventMessages) == 0 {
		t.Fatal("нет события messages")
	}
}

func (r *recorder) count(name string) int {
	n := 0
	for _, e := range r.all() {
		if e.name == name {
			n++
		}
	}
	return n
}

// Без сессии обработчик картинок отвечает 404.
func TestMediaWithoutSession(t *testing.T) {
	p := newParty(t, apitest.NewServer(), t.TempDir())
	w := httptest.NewRecorder()
	p.svc.Media().ServeHTTP(w, httptest.NewRequest("GET", "/media/1", nil))
	if w.Code != http.StatusNotFound || w.Body.Len() != 0 {
		t.Fatalf("%d", w.Code)
	}
}
