package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"
	"time"

	"cryptochat/client/internal/apitest"
	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/ciphers"
	"cryptochat/client/internal/rpcclient"
	"cryptochat/core/cipher"
)

// pairWithContacts — два вошедших участника-контакта.
func pairWithContacts(t *testing.T) (*apitest.Server, *party, *party) {
	t.Helper()
	srv := apitest.NewServer()
	a, b := registered(t, srv, "alice"), registered(t, srv, "bob")
	makeContacts(t, a, b)
	return srv, a, b
}

// serverChat создаёт чат напрямую через API от имени участника, минуя проверку доступности алгоритма.
func serverChat(t *testing.T, p *party, peer string, params rpcclient.ChatParams) [16]byte {
	t.Helper()
	c, err := p.api.CreateChat(context.Background(), peer, params)
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func findChat(p *party, id [16]byte) (ChatInfo, bool) {
	for _, c := range p.svc.Snapshot().Chats {
		if c.ID == id {
			return c, true
		}
	}
	return ChatInfo{}, false
}

func anyAvailable() (string, bool) {
	for _, o := range ciphers.Options().Algorithms {
		if o.Info.Available {
			return o.ID, true
		}
	}
	return "", false
}

// Доступность берётся из сборки: недоступный алгоритм отклоняется без обращения к серверу, доступный
// уходит на сервер идентификатором контракта, а не идентификатором подстановки.
func TestCreateChatAvailability(t *testing.T) {
	_, a, b := pairWithContacts(t)
	for _, o := range ciphers.Options().Algorithms {
		a.api.ResetCalls()
		c, err := a.svc.CreateChat(CreateChatRequest{Peer: "bob", Algorithm: o.ID, KeyBits: 192, CipherMode: "CTR", Padding: "ISO_10126"})
		if !o.Info.Available {
			if apperr.CodeOf(err) != apperr.AlgorithmUnavailable || a.api.Count("CreateChat") != 0 {
				t.Errorf("%s: %v, вызовов %d", o.ID, err, a.api.Count("CreateChat"))
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", o.ID, err)
		}
		if c.Algorithm != o.Algorithm || c.KeyBits != 192 || c.Mode != 6 || c.Padding != 4 || c.Peer != "bob" || c.Creator != "alice" {
			t.Errorf("%s: %+v", o.ID, c)
		}
		if o.Info.Substitute && c.AlgorithmNote == "" {
			t.Errorf("%s: нет пометки о подстановке", o.ID)
		}
		eventually(t, 2*time.Second, "чат у собеседника", func() bool {
			got, ok := findChat(b, c.ID)
			return ok && got.Algorithm == o.Algorithm && got.Mode == 6 && got.Padding == 4 && got.KeyBits == 192
		})
	}
}

func TestCreateChatValidationAndServerErrors(t *testing.T) {
	_, a, _ := pairWithContacts(t)
	alg, found := anyAvailable()
	if !found {
		alg = "RC6"
	}
	local := []struct {
		req   CreateChatRequest
		field string
	}{
		{CreateChatRequest{Peer: "bob", Algorithm: "AES", KeyBits: 128, CipherMode: "CBC", Padding: "PKCS7"}, "algorithm"},
		{CreateChatRequest{Peer: "bob", Algorithm: alg, KeyBits: 100, CipherMode: "CBC", Padding: "PKCS7"}, "key_bits"},
		{CreateChatRequest{Peer: "bob", Algorithm: alg, KeyBits: 128, CipherMode: "XTS", Padding: "PKCS7"}, "cipher_mode"},
		{CreateChatRequest{Peer: "bob", Algorithm: alg, KeyBits: 128, CipherMode: "CBC", Padding: "NONE"}, "padding_mode"},
	}
	a.api.ResetCalls()
	for _, c := range local {
		_, err := a.svc.CreateChat(c.req)
		e, ok := apperr.As(err)
		if !ok || e.Code != apperr.InvalidChatParams || e.Field != c.field {
			t.Errorf("%s: %v", c.field, err)
		}
	}
	if a.api.Count("CreateChat") != 0 {
		t.Fatal("недопустимые параметры дошли до сервера")
	}
}

// Ответы сервера при создании переводятся в коды клиента.
func TestCreateChatServerReasons(t *testing.T) {
	srv, a, _ := pairWithContacts(t)
	alg, ok := anyAvailable()
	if !ok {
		t.Skip("в сборке нет доступных алгоритмов: проверяется сборкой devcipher")
	}
	registered(t, srv, "carol")
	req := CreateChatRequest{Algorithm: alg, KeyBits: 256, CipherMode: "CBC", Padding: "PKCS7"}
	req.Peer = "carol"
	if _, err := a.svc.CreateChat(req); apperr.CodeOf(err) != apperr.NotContact {
		t.Errorf("не контакт: %v", err)
	}
	req.Peer = "alice"
	if _, err := a.svc.CreateChat(req); apperr.CodeOf(err) != apperr.SelfChat {
		t.Errorf("с собой: %v", err)
	}
	a.api.SetHook(func(m string) error {
		if m == "CreateChat" {
			return apperr.WithField(apperr.InvalidChatParams, "key_bits")
		}
		return nil
	})
	req.Peer = "bob"
	_, err := a.svc.CreateChat(req)
	if e, ok := apperr.As(err); !ok || e.Code != apperr.InvalidChatParams || e.Field != "key_bits" {
		t.Errorf("отказ сервера по параметру: %v", err)
	}
}

func TestChatListView(t *testing.T) {
	_, a, b := pairWithContacts(t)
	first := serverChat(t, b, "alice", rpcclient.ChatParams{Algorithm: cipher.MAGENTA, KeyBits: 128, Mode: 1, Padding: 1})
	second := serverChat(t, a, "bob", rpcclient.ChatParams{Algorithm: cipher.RC6, KeyBits: 256, Mode: 7, Padding: 3})
	eventually(t, 2*time.Second, "два чата", func() bool { return len(a.svc.Snapshot().Chats) == 2 })
	chats := a.svc.Snapshot().Chats
	if chats[0].ID != second || chats[1].ID != first {
		t.Fatal("новые чаты должны идти первыми")
	}
	c := chats[1]
	if c.Peer != "bob" || c.Creator != "bob" || c.Algorithm != cipher.MAGENTA || c.KeyBits != 128 || c.Mode != 1 ||
		c.Padding != 1 || c.SelfConnected || !c.PeerConnected || c.CreatedAt.IsZero() {
		t.Fatalf("%+v", c)
	}
	if st := ciphers.Info(cipher.MAGENTA); (st.Substitute || !st.Available) != (c.AlgorithmNote != "") {
		t.Fatalf("пометка алгоритма %q при %+v", c.AlgorithmNote, st)
	}
	if !chats[0].SelfConnected || chats[0].Creator != "alice" {
		t.Fatalf("%+v", chats[0])
	}
}

func TestCloseChatRemovesSecrets(t *testing.T) {
	_, a, b := pairWithContacts(t)
	id := serverChat(t, a, "bob", rpcclient.ChatParams{Algorithm: cipher.RC6, KeyBits: 128, Mode: 2, Padding: 3})
	eventually(t, 2*time.Second, "чат у обоих", func() bool {
		_, okA := findChat(a, id)
		_, okB := findChat(b, id)
		return okA && okB
	})
	db := a.svc.sess.db
	if err := db.SaveSessionKeyAndDropSecret(id, bytes.Repeat([]byte{7}, 16)); err != nil {
		t.Fatal(err)
	}
	a.api.ResetCalls()
	if err := a.svc.CloseChat(hex.EncodeToString(id[:])); err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.SessionKeyExists(id); ok {
		t.Fatal("ключ не удалён")
	}
	if _, ok := findChat(a, id); ok {
		t.Fatal("закрытый чат в списке")
	}
	if a.api.Count("ListChats") == 0 {
		t.Fatal("нет синхронизации после закрытия")
	}
	// Повтор безопасен, собеседник получает уведомление один раз.
	if err := a.svc.CloseChat(hex.EncodeToString(id[:])); err != nil {
		t.Fatalf("повторное закрытие: %v", err)
	}
	eventually(t, 2*time.Second, "уведомление собеседнику", func() bool { return len(b.rec.notices("CHAT_CLOSED_BY_PEER")) == 1 })
	time.Sleep(200 * time.Millisecond)
	if n := len(b.rec.notices("CHAT_CLOSED_BY_PEER")); n != 1 {
		t.Fatalf("уведомлений %d", n)
	}
	if len(a.rec.notices("CHAT_CLOSED_BY_PEER")) != 0 {
		t.Fatal("закрывшему пришло уведомление о закрытии собеседником")
	}
	if err := a.svc.CloseChat("zz"); apperr.CodeOf(err) != apperr.InvalidChatID {
		t.Fatalf("неверный идентификатор: %v", err)
	}
}

// Чат, закрытый собеседником, уходит из списка, его секреты удаляются, уведомление — один раз.
func TestPeerClosedChat(t *testing.T) {
	srv, a, _ := pairWithContacts(t)
	id := serverChat(t, a, "bob", rpcclient.ChatParams{Algorithm: cipher.RC6, KeyBits: 128, Mode: 2, Padding: 3})
	eventually(t, 2*time.Second, "чат", func() bool { _, ok := findChat(a, id); return ok })
	db := a.svc.sess.db
	// Создатель подключён: сервис сам создаёт и публикует секрет.
	eventually(t, 2*time.Second, "секрет создателя", func() bool { _, ok, _ := db.DHSecret(id); return ok })
	srv.CloseAs(id)
	eventually(t, 2*time.Second, "чат исчез", func() bool { _, ok := findChat(a, id); return !ok })
	if _, ok, _ := db.DHSecret(id); ok {
		t.Fatal("секрет не удалён")
	}
	if c, _, _ := db.Chat(id); c.ClosedAt.IsZero() {
		t.Fatal("чат не отмечен закрытым")
	}
	time.Sleep(200 * time.Millisecond)
	n := a.rec.notices("CHAT_CLOSED_BY_PEER")
	if len(n) != 1 || n[0].Kind != NoticeInfo {
		t.Fatalf("уведомления: %+v", n)
	}
}

// Сервер больше не знает о чате (CHAT_NOT_FOUND): то же, но без уточнения собеседника.
func TestChatForgottenByServer(t *testing.T) {
	srv, a, _ := pairWithContacts(t)
	id := serverChat(t, a, "bob", rpcclient.ChatParams{Algorithm: cipher.RC6, KeyBits: 128, Mode: 2, Padding: 3})
	eventually(t, 2*time.Second, "чат", func() bool { _, ok := findChat(a, id); return ok })
	db := a.svc.sess.db
	if err := db.SaveSessionKeyAndDropSecret(id, bytes.Repeat([]byte{7}, 16)); err != nil {
		t.Fatal(err)
	}
	srv.Forget(id)
	eventually(t, 2*time.Second, "чат исчез", func() bool { _, ok := findChat(a, id); return !ok })
	if ok, _ := db.SessionKeyExists(id); ok {
		t.Fatal("ключ не удалён")
	}
	time.Sleep(200 * time.Millisecond)
	if len(a.rec.notices(apperr.ChatNotFound)) != 1 || len(a.rec.notices("CHAT_CLOSED_BY_PEER")) != 0 {
		t.Fatalf("уведомления: %+v", a.rec.notices(""))
	}
}

func TestChatOptionsFromBuild(t *testing.T) {
	p := newParty(t, apitest.NewServer(), t.TempDir())
	if o := p.svc.ChatOptions(); len(o.Algorithms) != 2 || len(o.CipherModes) != 7 || len(o.Paddings) != 4 {
		t.Fatalf("%+v", o)
	}
}
