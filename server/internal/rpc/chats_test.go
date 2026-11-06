package rpc

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"

	apiv1 "cryptochat/server/api/v1"
)

func goodParams() *apiv1.ChatParams {
	return &apiv1.ChatParams{
		Algorithm:   apiv1.Algorithm_ALGORITHM_RC6,
		KeyBits:     256,
		CipherMode:  apiv1.CipherMode_CIPHER_MODE_CBC,
		PaddingMode: apiv1.PaddingMode_PADDING_MODE_PKCS7,
	}
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// friends создаёт двух контактов и возвращает их сессии.
func friends(t *testing.T, h *harness) (string, string) {
	t.Helper()
	a, _ := h.signUp(t, "alice")
	b, _ := h.signUp(t, "bob")
	if _, err := h.contacts.SendRequest(auth(a), &apiv1.SendContactRequest{Login: "bob"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.contacts.AcceptRequest(auth(b), &apiv1.ContactLogin{Login: "alice"}); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func createChat(t *testing.T, h *harness, token string) []byte {
	t.Helper()
	r, err := h.chats.CreateChat(auth(token), &apiv1.CreateChatRequest{PeerLogin: "bob", Params: goodParams()})
	if err != nil {
		t.Fatal(err)
	}
	return r.GetChat().GetChatId()
}

func TestChatLifecycle(t *testing.T) {
	h := start(t, nil)
	a, b := friends(t, h)
	r, err := h.chats.CreateChat(auth(a), &apiv1.CreateChatRequest{PeerLogin: "BOB", Params: goodParams()})
	if err != nil {
		t.Fatal(err)
	}
	chat := r.GetChat()
	if len(chat.GetChatId()) != 16 || chat.GetCreator() != "alice" || chat.GetClosedAt() != nil || chat.GetCreatedAt() == nil || chat.GetReady() {
		t.Fatalf("чат %v", chat)
	}
	if chat.GetParams().GetKeyBits() != 256 || chat.GetParams().GetAlgorithm() != apiv1.Algorithm_ALGORITHM_RC6 ||
		chat.GetParams().GetCipherMode() != apiv1.CipherMode_CIPHER_MODE_CBC || chat.GetParams().GetPaddingMode() != apiv1.PaddingMode_PADDING_MODE_PKCS7 {
		t.Fatalf("параметры %v", chat.GetParams())
	}
	if chat.GetSelf().GetLogin() != "alice" || chat.GetPeer().GetLogin() != "bob" || !chat.GetSelf().GetConnected() || chat.GetPeer().GetConnected() {
		t.Fatalf("участники %v, %v", chat.GetSelf(), chat.GetPeer())
	}
	list, err := h.chats.ListChats(auth(b), &apiv1.ListChatsRequest{})
	if err != nil || len(list.GetChats()) != 1 {
		t.Fatalf("список: %v, %v", list, err)
	}
	seen := list.GetChats()[0]
	if !bytes.Equal(seen.GetChatId(), chat.GetChatId()) || seen.GetSelf().GetLogin() != "bob" || seen.GetPeer().GetLogin() != "alice" {
		t.Fatalf("у собеседника %v", seen)
	}
	ref := &apiv1.ChatRef{ChatId: chat.GetChatId()}
	closed, err := h.chats.CloseChat(auth(b), ref)
	if err != nil || closed.GetChat().GetClosedAt() == nil {
		t.Fatalf("закрытие: %v, %v", closed, err)
	}
	list, err = h.chats.ListChats(auth(a), &apiv1.ListChatsRequest{})
	if err != nil || len(list.GetChats()) != 0 {
		t.Fatalf("закрытый чат в списке: %v, %v", list, err)
	}
	got, err := h.chats.GetChat(auth(a), ref)
	if err != nil || got.GetChat().GetClosedAt() == nil {
		t.Fatalf("закрытый чат: %v, %v", got, err)
	}
	if _, err := h.chats.CloseChat(auth(a), ref); err != nil {
		t.Fatalf("повторное закрытие: %v", err)
	}
}

func TestCreateChatErrors(t *testing.T) {
	h := start(t, nil)
	a, _ := friends(t, h)
	stranger, _ := h.signUp(t, "carol")
	_, err := h.chats.CreateChat(auth(a), &apiv1.CreateChatRequest{PeerLogin: "carol", Params: goodParams()})
	expectError(t, err, codes.FailedPrecondition, ReasonNotContact)
	_, missing := h.chats.CreateChat(auth(a), &apiv1.CreateChatRequest{PeerLogin: "nobody", Params: goodParams()})
	expectError(t, missing, codes.FailedPrecondition, ReasonNotContact)
	if err.Error() != missing.Error() {
		t.Errorf("создание раскрывает логины:\n%v\n%v", err, missing)
	}
	_, err = h.chats.CreateChat(auth(a), &apiv1.CreateChatRequest{PeerLogin: "alice", Params: goodParams()})
	expectError(t, err, codes.InvalidArgument, ReasonSelfChat)

	for field, edit := range map[string]func(*apiv1.ChatParams){
		"algorithm":    func(p *apiv1.ChatParams) { p.Algorithm = 0 },
		"key_bits":     func(p *apiv1.ChatParams) { p.KeyBits = 100 },
		"cipher_mode":  func(p *apiv1.ChatParams) { p.CipherMode = 99 },
		"padding_mode": func(p *apiv1.ChatParams) { p.PaddingMode = 0 },
	} {
		p := goodParams()
		edit(p)
		_, err := h.chats.CreateChat(auth(a), &apiv1.CreateChatRequest{PeerLogin: "bob", Params: p})
		expectError(t, err, codes.InvalidArgument, ReasonInvalidChatParams)
		if FieldOf(err) != field {
			t.Errorf("поле %q, ожидалось %q", FieldOf(err), field)
		}
	}
	_, err = h.chats.CreateChat(auth(a), &apiv1.CreateChatRequest{PeerLogin: "bob"})
	expectError(t, err, codes.InvalidArgument, ReasonInvalidChatParams)

	_, err = h.chats.GetChat(auth(stranger), &apiv1.ChatRef{ChatId: []byte{1, 2, 3}})
	expectError(t, err, codes.InvalidArgument, ReasonInvalidChatID)
	_, err = h.keys.GetPeerPublicValue(auth(stranger), &apiv1.ChatRef{})
	expectError(t, err, codes.InvalidArgument, ReasonInvalidChatID)
}

// FR-018: все 8 методов закрыты без сессии.
func TestChatMethodsRequireSession(t *testing.T) {
	h := start(t, nil)
	ref := &apiv1.ChatRef{ChatId: make([]byte, 16)}
	calls := map[string]func(context.Context) error{
		"CreateChat": func(c context.Context) error {
			_, err := h.chats.CreateChat(c, &apiv1.CreateChatRequest{PeerLogin: "bob", Params: goodParams()})
			return err
		},
		"ListChats": func(c context.Context) error {
			_, err := h.chats.ListChats(c, &apiv1.ListChatsRequest{})
			return err
		},
		"GetChat":        func(c context.Context) error { _, err := h.chats.GetChat(c, ref); return err },
		"ConnectChat":    func(c context.Context) error { _, err := h.chats.ConnectChat(c, ref); return err },
		"DisconnectChat": func(c context.Context) error { _, err := h.chats.DisconnectChat(c, ref); return err },
		"CloseChat":      func(c context.Context) error { _, err := h.chats.CloseChat(c, ref); return err },
		"PublishPublicValue": func(c context.Context) error {
			_, err := h.keys.PublishPublicValue(c, &apiv1.PublishPublicValueRequest{ChatId: ref.ChatId, PublicValue: make([]byte, 256)})
			return err
		},
		"GetPeerPublicValue": func(c context.Context) error { _, err := h.keys.GetPeerPublicValue(c, ref); return err },
	}
	if len(calls) != 8 {
		t.Fatalf("методов %d", len(calls))
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			expectError(t, call(bg), codes.Unauthenticated, ReasonUnauthenticated)
			expectError(t, call(auth("unknown-session")), codes.Unauthenticated, ReasonUnauthenticated)
		})
	}
}

// SC-003: для постороннего чужой чат не отличается от несуществующего ни в одном методе.
func TestStrangerSeesNotFound(t *testing.T) {
	h := start(t, nil)
	a, _ := friends(t, h)
	stranger, _ := h.signUp(t, "carol")
	id := createChat(t, h, a)
	real, fake := &apiv1.ChatRef{ChatId: id}, &apiv1.ChatRef{ChatId: randomBytes(t, 16)}
	value := randomBytes(t, 256)
	calls := map[string]func(*apiv1.ChatRef) error{
		"GetChat":        func(r *apiv1.ChatRef) error { _, err := h.chats.GetChat(auth(stranger), r); return err },
		"ConnectChat":    func(r *apiv1.ChatRef) error { _, err := h.chats.ConnectChat(auth(stranger), r); return err },
		"DisconnectChat": func(r *apiv1.ChatRef) error { _, err := h.chats.DisconnectChat(auth(stranger), r); return err },
		"CloseChat":      func(r *apiv1.ChatRef) error { _, err := h.chats.CloseChat(auth(stranger), r); return err },
		"PublishPublicValue": func(r *apiv1.ChatRef) error {
			_, err := h.keys.PublishPublicValue(auth(stranger), &apiv1.PublishPublicValueRequest{ChatId: r.GetChatId(), PublicValue: value})
			return err
		},
		"GetPeerPublicValue": func(r *apiv1.ChatRef) error { _, err := h.keys.GetPeerPublicValue(auth(stranger), r); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			onReal, onFake := call(real), call(fake)
			expectError(t, onReal, codes.NotFound, ReasonChatNotFound)
			if onReal.Error() != onFake.Error() {
				t.Errorf("ответы различаются:\n%v\n%v", onReal, onFake)
			}
		})
	}
	got, err := h.chats.GetChat(auth(a), real)
	if err != nil || got.GetChat().GetClosedAt() != nil {
		t.Fatalf("посторонний изменил чат: %v, %v", got, err)
	}
}

func TestConnectDisconnect(t *testing.T) {
	h := start(t, nil)
	a, b := friends(t, h)
	ref := &apiv1.ChatRef{ChatId: createChat(t, h, a)}
	r, err := h.chats.ConnectChat(auth(b), ref)
	if err != nil || !r.GetChat().GetSelf().GetConnected() || !r.GetChat().GetPeer().GetConnected() {
		t.Fatalf("подключение: %v, %v", r, err)
	}
	if _, err := h.chats.ConnectChat(auth(b), ref); err != nil {
		t.Fatalf("повтор: %v", err)
	}
	r, err = h.chats.DisconnectChat(auth(a), ref)
	if err != nil || r.GetChat().GetSelf().GetConnected() || r.GetChat().GetClosedAt() != nil {
		t.Fatalf("отключение: %v, %v", r, err)
	}
	if _, err := h.chats.DisconnectChat(auth(a), ref); err != nil {
		t.Fatalf("повтор: %v", err)
	}
	if _, err := h.chats.CloseChat(auth(b), ref); err != nil {
		t.Fatal(err)
	}
	_, err = h.chats.ConnectChat(auth(a), ref)
	expectError(t, err, codes.FailedPrecondition, ReasonChatClosed)
	if _, err := h.chats.DisconnectChat(auth(a), ref); err != nil {
		t.Fatalf("отключение от закрытого: %v", err)
	}
}

func TestKeyExchange(t *testing.T) {
	h := start(t, nil)
	a, b := friends(t, h)
	id := createChat(t, h, a)
	ref := &apiv1.ChatRef{ChatId: id}
	fromA, fromB := randomBytes(t, 256), randomBytes(t, 256)

	peer, err := h.keys.GetPeerPublicValue(auth(b), ref)
	if err != nil || peer.GetPublished() || len(peer.GetPublicValue()) != 0 {
		t.Fatalf("значения ещё нет: %v, %v", peer, err)
	}
	publish := func(token string, value []byte) error {
		_, err := h.keys.PublishPublicValue(auth(token), &apiv1.PublishPublicValueRequest{ChatId: id, PublicValue: value})
		return err
	}
	expectError(t, publish(b, fromB), codes.FailedPrecondition, ReasonNotConnected)
	for _, length := range []int{0, 255, 257} {
		expectError(t, publish(a, randomBytes(t, length)), codes.InvalidArgument, ReasonInvalidPublicValue)
	}
	if err := publish(a, fromA); err != nil {
		t.Fatal(err)
	}
	if err := publish(a, fromA); err != nil {
		t.Fatalf("повтор того же значения: %v", err)
	}
	expectError(t, publish(a, fromB), codes.AlreadyExists, ReasonValueAlreadyPublished)

	if _, err := h.chats.ConnectChat(auth(b), ref); err != nil {
		t.Fatal(err)
	}
	if err := publish(b, fromB); err != nil {
		t.Fatal(err)
	}
	got, err := h.chats.GetChat(auth(a), ref)
	if err != nil || !got.GetChat().GetReady() || !got.GetChat().GetPeer().GetPublicValuePublished() {
		t.Fatalf("готовность: %v, %v", got, err)
	}
	peer, err = h.keys.GetPeerPublicValue(auth(b), ref)
	if err != nil || !peer.GetPublished() || !bytes.Equal(peer.GetPublicValue(), fromA) {
		t.Fatalf("значение создателя: %v", err)
	}
	peer, err = h.keys.GetPeerPublicValue(auth(a), ref)
	if err != nil || !peer.GetPublished() || !bytes.Equal(peer.GetPublicValue(), fromB) {
		t.Fatalf("значение собеседника: %v", err)
	}

	// FR-022: значений нет в журнале ни в каком виде.
	journal := h.log.String()
	for _, value := range [][]byte{fromA, fromB} {
		if strings.Contains(journal, hex.EncodeToString(value)) || strings.Contains(journal, string(value)) {
			t.Error("журнал содержит открытое значение")
		}
	}
	if strings.Contains(journal, hex.EncodeToString(id)) {
		t.Error("журнал содержит идентификатор чата")
	}

	if _, err := h.chats.CloseChat(auth(a), ref); err != nil {
		t.Fatal(err)
	}
	_, err = h.keys.GetPeerPublicValue(auth(b), ref)
	expectError(t, err, codes.FailedPrecondition, ReasonChatClosed)
	expectError(t, publish(a, fromA), codes.FailedPrecondition, ReasonChatClosed)
	got, err = h.chats.GetChat(auth(a), ref)
	if err != nil || got.GetChat().GetReady() || got.GetChat().GetSelf().GetPublicValuePublished() {
		t.Fatalf("после закрытия: %v, %v", got, err)
	}
}

func TestRemoveContactClosesChats(t *testing.T) {
	h := start(t, nil)
	a, b := friends(t, h)
	ref := &apiv1.ChatRef{ChatId: createChat(t, h, a)}
	if _, err := h.contacts.RemoveContact(auth(b), &apiv1.ContactLogin{Login: "alice"}); err != nil {
		t.Fatal(err)
	}
	got, err := h.chats.GetChat(auth(a), ref)
	if err != nil || got.GetChat().GetClosedAt() == nil {
		t.Fatalf("чат не закрыт: %v, %v", got, err)
	}
}

// Все значения перечислений API принимаются, а следующее за последним отвергается.
func TestEnumsMatchValidation(t *testing.T) {
	h := start(t, nil)
	a, _ := friends(t, h)
	create := func(p *apiv1.ChatParams) error {
		_, err := h.chats.CreateChat(auth(a), &apiv1.CreateChatRequest{PeerLogin: "bob", Params: p})
		return err
	}
	for value := range apiv1.Algorithm_name {
		p := goodParams()
		p.Algorithm = apiv1.Algorithm(value)
		if err := create(p); (value == 0) != (err != nil) {
			t.Errorf("алгоритм %d: %v", value, err)
		}
	}
	for value := range apiv1.CipherMode_name {
		p := goodParams()
		p.CipherMode = apiv1.CipherMode(value)
		if err := create(p); (value == 0) != (err != nil) {
			t.Errorf("режим %d: %v", value, err)
		}
	}
	for value := range apiv1.PaddingMode_name {
		p := goodParams()
		p.PaddingMode = apiv1.PaddingMode(value)
		if err := create(p); (value == 0) != (err != nil) {
			t.Errorf("набивка %d: %v", value, err)
		}
	}
	p := goodParams()
	p.Algorithm = apiv1.Algorithm(len(apiv1.Algorithm_name))
	expectError(t, create(p), codes.InvalidArgument, ReasonInvalidChatParams)
	p = goodParams()
	p.CipherMode = apiv1.CipherMode(len(apiv1.CipherMode_name))
	expectError(t, create(p), codes.InvalidArgument, ReasonInvalidChatParams)
	p = goodParams()
	p.PaddingMode = apiv1.PaddingMode(len(apiv1.PaddingMode_name))
	expectError(t, create(p), codes.InvalidArgument, ReasonInvalidChatParams)
}
