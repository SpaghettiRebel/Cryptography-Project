package keyagree

import (
	"bytes"
	"context"
	"math/big"
	"path/filepath"
	"testing"

	"cryptochat/client/internal/apitest"
	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/localdb"
	"cryptochat/client/internal/rpcclient"
	"cryptochat/core/cipher"
	"cryptochat/core/dh"
	"cryptochat/core/kdf"
)

var ctx = context.Background()

type env struct {
	t     *testing.T
	srv   *apitest.Server
	alice *apitest.Client
	bob   *apitest.Client
	db    *localdb.DB
	id    [16]byte
}

func login(t *testing.T, srv *apitest.Server, name string) *apitest.Client {
	t.Helper()
	c := srv.Client()
	if err := c.Register(ctx, name, "password-1"); err != nil {
		t.Fatal(err)
	}
	s, err := c.Login(ctx, name, "password-1")
	if err != nil {
		t.Fatal(err)
	}
	c.SetToken(s.Token)
	return c
}

// newEnv: alice создаёт чат с bob; локальная база alice содержит строку чата.
func newEnv(t *testing.T, alg cipher.Algorithm, bits int) *env {
	t.Helper()
	srv := apitest.NewServer()
	e := &env{t: t, srv: srv, alice: login(t, srv, "alice"), bob: login(t, srv, "bob")}
	if _, err := e.alice.SendContactRequest(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := e.bob.AcceptContact(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	c, err := e.alice.CreateChat(ctx, "bob", rpcclient.ChatParams{Algorithm: alg, KeyBits: bits, Mode: 2, Padding: 3})
	if err != nil {
		t.Fatal(err)
	}
	e.id = c.ID
	db, err := localdb.Open(filepath.Join(t.TempDir(), "client.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e.db = db
	if err := db.UpsertChat(localdb.Chat{ID: c.ID, Peer: "bob", Creator: "alice", Algorithm: alg, KeyBits: bits, Mode: 2, Padding: 3, CreatedAt: c.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) chat() rpcclient.Chat {
	e.t.Helper()
	c, err := e.alice.GetChat(ctx, e.id)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) advance() (State, error) {
	e.t.Helper()
	return Advance(ctx, e.alice, e.db, e.chat())
}

// bobPublishes подключает bob и публикует его значение; возвращает пару bob.
func (e *env) bobPublishes() *dh.KeyPair {
	e.t.Helper()
	if _, err := e.bob.ConnectChat(ctx, e.id); err != nil {
		e.t.Fatal(err)
	}
	pair, err := dh.Group14().GenerateKey()
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.bob.PublishPublicValue(ctx, e.id, pair.PublicBytes()); err != nil {
		e.t.Fatal(err)
	}
	return pair
}

func TestSecretSavedBeforePublish(t *testing.T) {
	e := newEnv(t, cipher.RC6, 256)
	checked := false
	e.alice.SetHook(func(m string) error {
		if m == "PublishPublicValue" {
			s, ok, err := e.db.DHSecret(e.id)
			if err != nil || !ok || s.Published || len(s.Private) != 256 || len(s.Public) != 256 {
				t.Errorf("в момент публикации секрет не записан: %v %v", ok, err)
			}
			checked = true
		}
		return nil
	})
	st, err := e.advance()
	if err != nil || st != WaitingPeer || !checked {
		t.Fatalf("%s %v %v", st, err, checked)
	}
	s, ok, _ := e.db.DHSecret(e.id)
	if !ok || !s.Published {
		t.Fatal("published_at не записан после успеха")
	}
	if !bytes.Equal(e.srv.Value(e.id, "alice"), s.Public) {
		t.Fatal("на сервере не то значение")
	}
}

func TestPublishRetrySendsSameValue(t *testing.T) {
	e := newEnv(t, cipher.RC6, 128)
	fail := true
	e.alice.SetHook(func(m string) error {
		if m == "PublishPublicValue" && fail {
			fail = false
			return apperr.New(apperr.ServerUnavailable)
		}
		return nil
	})
	st, err := e.advance()
	if st != WaitingSelf || apperr.CodeOf(err) != apperr.ServerUnavailable {
		t.Fatalf("%s %v", st, err)
	}
	first, _, _ := e.db.DHSecret(e.id)
	if st, err := e.advance(); err != nil || st != WaitingPeer {
		t.Fatalf("повтор: %s %v", st, err)
	}
	second, _, _ := e.db.DHSecret(e.id)
	if !bytes.Equal(first.Private, second.Private) || !bytes.Equal(e.srv.Value(e.id, "alice"), first.Public) {
		t.Fatal("повтор публикует другое значение")
	}
	// Повтор после подтверждённой публикации (ответ потерян) — то же значение, ошибки нет.
	if err := e.alice.PublishPublicValue(ctx, e.id, first.Public); err != nil {
		t.Fatal(err)
	}
	if e.srv.Publications() != 1 {
		t.Fatalf("публикаций %d", e.srv.Publications())
	}
}

func TestKeyDerivedWithChatAlgorithm(t *testing.T) {
	for _, alg := range []cipher.Algorithm{cipher.MAGENTA, cipher.RC6} {
		for _, bits := range []int{128, 192, 256} {
			e := newEnv(t, alg, bits)
			if st, err := e.advance(); err != nil || st != WaitingPeer {
				t.Fatalf("%s %v", st, err)
			}
			own, _, _ := e.db.DHSecret(e.id)
			bob := e.bobPublishes()
			st, err := e.advance()
			if err != nil || st != Ready {
				t.Fatalf("%v/%d: %s %v", alg, bits, st, err)
			}
			if _, ok, _ := e.db.DHSecret(e.id); ok {
				t.Fatal("секрет не удалён после выработки ключа")
			}
			key, ok, err := e.db.SessionKey(e.id)
			if err != nil || !ok || len(key) != bits/8 {
				t.Fatalf("ключ: %d %v", len(key), err)
			}
			ownPub, err := dh.Group14().ParsePublic(own.Public)
			if err != nil {
				t.Fatal(err)
			}
			shared, err := bob.SharedSecret(ownPub)
			if err != nil {
				t.Fatal(err)
			}
			want, err := kdf.SessionKey(shared, e.id, alg, bits)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(key, want) {
				t.Fatalf("%v/%d: ключ не совпадает с ключом собеседника", alg, bits)
			}
			other, _ := kdf.SessionKey(shared, e.id, cipher.Algorithm(255), bits)
			if bytes.Equal(key, other) {
				t.Fatal("ключ выведен с идентификатором подстановки")
			}
		}
	}
}

func TestKeyLostNoRepublish(t *testing.T) {
	e := newEnv(t, cipher.RC6, 256)
	if _, err := e.advance(); err != nil {
		t.Fatal(err)
	}
	// Профиль потерян: секрета и ключа нет, а сервер помнит опубликованное значение.
	if err := e.db.DeleteChatSecrets(e.id); err != nil {
		t.Fatal(err)
	}
	before := e.srv.Value(e.id, "alice")
	e.alice.ResetCalls()
	st, err := e.advance()
	if err != nil || st != KeyLost {
		t.Fatalf("%s %v", st, err)
	}
	if e.alice.Count("PublishPublicValue") != 0 {
		t.Fatal("опубликовано другое значение")
	}
	if !bytes.Equal(e.srv.Value(e.id, "alice"), before) {
		t.Fatal("значение на сервере изменилось")
	}
	if _, ok, _ := e.db.DHSecret(e.id); ok {
		t.Fatal("создан новый секрет")
	}
}

func TestInvalidPeerValues(t *testing.T) {
	p := dh.Group14().P()
	bad := map[string][]byte{
		"0":        make([]byte, 256),
		"1":        big.NewInt(1).FillBytes(make([]byte, 256)),
		"p-1":      new(big.Int).Sub(p, big.NewInt(1)).FillBytes(make([]byte, 256)),
		"255 байт": bytes.Repeat([]byte{5}, 255),
	}
	for name, v := range bad {
		e := newEnv(t, cipher.RC6, 128)
		if _, err := e.advance(); err != nil {
			t.Fatal(err)
		}
		e.srv.SetValue(e.id, "bob", v)
		st, err := e.advance()
		if st != PeerValueInvalid || apperr.CodeOf(err) != apperr.PeerValueInvalid {
			t.Errorf("%s: %s %v", name, st, err)
		}
		if _, ok, _ := e.db.DHSecret(e.id); !ok {
			t.Errorf("%s: секрет удалён", name)
		}
		if ok, _ := e.db.SessionKeyExists(e.id); ok {
			t.Errorf("%s: ключ выработан", name)
		}
	}
}

func TestClosedAndMissingChatsDropSecrets(t *testing.T) {
	e := newEnv(t, cipher.RC6, 128)
	if _, err := e.advance(); err != nil {
		t.Fatal(err)
	}
	c := e.chat()
	e.srv.CloseAs(e.id)
	closed := e.chat()
	if _, err := Advance(ctx, e.alice, e.db, closed); apperr.CodeOf(err) != apperr.ChatClosed {
		t.Fatalf("закрытый чат: %v", err)
	}
	if _, ok, _ := e.db.DHSecret(e.id); ok {
		t.Fatal("секрет закрытого чата не удалён")
	}

	e2 := newEnv(t, cipher.RC6, 128)
	if _, err := e2.advance(); err != nil {
		t.Fatal(err)
	}
	c = e2.chat()
	e2.srv.Forget(e2.id)
	if _, err := Advance(ctx, e2.alice, e2.db, c); apperr.CodeOf(err) != apperr.ChatNotFound {
		t.Fatalf("исчезнувший чат: %v", err)
	}
	if _, ok, _ := e2.db.DHSecret(e2.id); ok {
		t.Fatal("секрет исчезнувшего чата не удалён")
	}
}

func TestReadyMakesNoCalls(t *testing.T) {
	e := newEnv(t, cipher.MAGENTA, 192)
	if err := e.db.SaveSessionKeyAndDropSecret(e.id, bytes.Repeat([]byte{1}, 24)); err != nil {
		t.Fatal(err)
	}
	c := e.chat()
	e.alice.ResetCalls()
	if st, err := Advance(ctx, e.alice, e.db, c); err != nil || st != Ready {
		t.Fatalf("%s %v", st, err)
	}
	if n := len(e.alice.Calls()); n != 0 {
		t.Fatalf("вызовы при готовом ключе: %v", e.alice.Calls())
	}
}

func TestNotConnected(t *testing.T) {
	e := newEnv(t, cipher.RC6, 128)
	if _, err := e.alice.DisconnectChat(ctx, e.id); err != nil {
		t.Fatal(err)
	}
	e.alice.ResetCalls()
	st, err := e.advance()
	if err != nil || st != None {
		t.Fatalf("%s %v", st, err)
	}
	if _, ok, _ := e.db.DHSecret(e.id); ok || e.alice.Count("PublishPublicValue") != 0 {
		t.Fatal("без подключения создан секрет или опубликовано значение")
	}

	// Опубликовано до отключения: значение собеседника запрашивается, ключ вырабатывается.
	if _, err := e.alice.ConnectChat(ctx, e.id); err != nil {
		t.Fatal(err)
	}
	if st, err := e.advance(); err != nil || st != WaitingPeer {
		t.Fatalf("%s %v", st, err)
	}
	if _, err := e.alice.DisconnectChat(ctx, e.id); err != nil {
		t.Fatal(err)
	}
	e.bobPublishes()
	e.alice.ResetCalls()
	st, err = e.advance()
	if err != nil || st != Ready || e.alice.Count("GetPeerPublicValue") != 1 {
		t.Fatalf("%s %v %v", st, err, e.alice.Calls())
	}
}

func TestLocalState(t *testing.T) {
	e := newEnv(t, cipher.RC6, 128)
	if LocalState(e.db, e.id) != None {
		t.Fatal("NONE")
	}
	if err := e.db.SaveDHSecret(e.id, bytes.Repeat([]byte{1}, 256), bytes.Repeat([]byte{2}, 256)); err != nil {
		t.Fatal(err)
	}
	if LocalState(e.db, e.id) != WaitingSelf {
		t.Fatal("WAITING_SELF")
	}
	_ = e.db.MarkPublished(e.id)
	if LocalState(e.db, e.id) != WaitingPeer {
		t.Fatal("WAITING_PEER")
	}
	_ = e.db.SaveSessionKeyAndDropSecret(e.id, bytes.Repeat([]byte{1}, 16))
	if LocalState(e.db, e.id) != Ready {
		t.Fatal("READY")
	}
}
