//go:build devcipher

package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"cryptochat/client/internal/apitest"
	"cryptochat/client/internal/localdb"
	"cryptochat/client/internal/rpcclient"
	"cryptochat/client/internal/service"
	"cryptochat/core/cipher"
	"cryptochat/core/protocol"
)

// JSON новых типов и событий переписки не содержит ключа, шифротекста и message_id ни в hex, ни в base64;
// текст и имя файла есть только там, где их показывает интерфейс.
func TestMessageDTOsHaveNoSecrets(t *testing.T) {
	srv := apitest.NewServer()
	var (
		mu     sync.Mutex
		events []string
		wires  [][]byte
	)
	type sideT struct {
		app *App
		api *apitest.Client
		db  *localdb.DB
	}
	newSide := func(login string) *sideT {
		s := &sideT{api: srv.Client()}
		svc := service.New(service.Config{
			Root: t.TempDir(),
			Dial: func(string, string) (rpcclient.API, error) { return s.api, nil },
			OpenStore: func(path string) (*localdb.DB, error) {
				d, err := localdb.Open(path)
				s.db = d
				return d, err
			},
			Interval: 50 * time.Millisecond,
			Logger:   log.New(io.Discard, "", 0),
		})
		t.Cleanup(svc.Close)
		svc.Subscribe(service.EmitterFunc(func(event string, data any) {
			b, _ := json.Marshal(eventView(data))
			mu.Lock()
			events = append(events, string(b))
			mu.Unlock()
		}))
		s.app = NewApp(svc)
		s.app.log = log.New(io.Discard, "", 0)
		if err := s.app.Register(login, "password-123"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.app.Login(login, "password-123"); err != nil {
			t.Fatal(err)
		}
		s.api.SetSendHook(func(_ int, w []byte) (bool, error) {
			mu.Lock()
			wires = append(wires, append([]byte(nil), w...))
			mu.Unlock()
			return true, nil
		})
		return s
	}
	a, b := newSide("alice"), newSide("bob")
	if _, err := a.app.SendContactRequest("bob"); err != nil {
		t.Fatal(err)
	}
	if err := b.app.AcceptContact("alice"); err != nil {
		t.Fatal(err)
	}
	c, err := a.api.CreateChat(context.Background(), "bob", rpcclient.ChatParams{Algorithm: cipher.RC6, KeyBits: 256, Mode: 2, Padding: 3})
	if err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(c.ID[:])
	wait := func(what string, cond func() bool) {
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatal("не дождались: " + what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	wait("чат у bob", func() bool { s, _ := b.app.GetSnapshot(); return len(s.Chats) == 1 })
	if _, err := b.app.ConnectChat(id); err != nil {
		t.Fatal(err)
	}
	wait("отправка доступна", func() bool {
		s, _ := a.app.GetSnapshot()
		return len(s.Chats) == 1 && s.Chats[0].CanSend
	})
	const text = "ТЕКСТ-МАРКЕР-7f3a"
	if _, err := a.app.SendText(id, text); err != nil {
		t.Fatal(err)
	}
	var page MessagePage
	wait("текст у bob", func() bool {
		page, _ = b.app.ListMessages(id, 0, 50)
		return len(page.Messages) == 1 && page.Messages[0].State == "DONE"
	})
	key, _, _ := a.db.SessionKey(c.ID)
	views := []any{page}
	pa, _ := a.app.ListMessages(id, 0, 50)
	views = append(views, pa)
	snapA, _ := a.app.GetSnapshot()
	snapB, _ := b.app.GetSnapshot()
	closed, _ := a.app.ListClosedChats()
	views = append(views, snapA, snapB, closed)
	var texts []string
	for _, v := range views {
		raw, _ := json.Marshal(v)
		texts = append(texts, string(raw))
	}
	mu.Lock()
	texts = append(texts, events...)
	captured := append([][]byte(nil), wires...)
	mu.Unlock()
	if len(captured) == 0 || len(key) == 0 {
		t.Fatal("нечего искать")
	}
	var needles [][]byte
	needles = append(needles, key)
	for _, w := range captured {
		f, err := protocol.Unmarshal(w)
		if err != nil {
			t.Fatal(err)
		}
		needles = append(needles, f.MessageID[:], f.IV, f.Cipher[:16])
	}
	for _, text := range texts {
		for _, n := range needles {
			for _, enc := range []string{hex.EncodeToString(n), strings.ToUpper(hex.EncodeToString(n)),
				base64.StdEncoding.EncodeToString(n), base64.RawStdEncoding.EncodeToString(n), base64.RawURLEncoding.EncodeToString(n)} {
				if len(enc) >= 16 && strings.Contains(text, enc) {
					t.Fatalf("данные для интерфейса содержат секрет или шифротекст: %.100s…", text)
				}
			}
		}
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, text) || !strings.Contains(joined, `"messages"`) && !strings.Contains(joined, `"changed"`) {
		t.Fatal("в данных нет текста сообщения или события messages")
	}
}
