package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cryptochat/client/internal/apitest"
	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/localdb"
	"cryptochat/client/internal/rpcclient"
	"cryptochat/core/cipher"
)

// listHook — соединение, в котором тест может подменить или задержать ответ ListChats.
type listHook struct {
	*apitest.Client
	mu    sync.Mutex
	after func([]rpcclient.Chat) []rpcclient.Chat
}

func (l *listHook) ListChats(ctx context.Context) ([]rpcclient.Chat, error) {
	out, err := l.Client.ListChats(ctx)
	l.mu.Lock()
	f := l.after
	l.mu.Unlock()
	if err == nil && f != nil {
		out = f(out)
	}
	return out, err
}

func (l *listHook) set(f func([]rpcclient.Chat) []rpcclient.Chat) {
	l.mu.Lock()
	l.after = f
	l.mu.Unlock()
}

// hookedParty — вошедший участник с перехватом ListChats.
func hookedParty(t *testing.T, srv *apitest.Server, login string) (*party, *listHook) {
	t.Helper()
	var hook *listHook
	p := newPartyWith(t, srv, t.TempDir(), func(c *apitest.Client) rpcclient.API {
		if hook == nil {
			hook = &listHook{Client: c}
		}
		return hook
	})
	if err := p.svc.Register(login, testPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := p.svc.Login(login, testPassword); err != nil {
		t.Fatal(err)
	}
	return p, hook
}

// waitCycles ждёт n полных циклов синхронизации участника.
func waitCycles(t *testing.T, p *party, n int) {
	t.Helper()
	start := p.api.Count("ListChats")
	eventually(t, 3*time.Second, "циклы синхронизации", func() bool { return p.api.Count("ListChats") >= start+n })
}

// Синхронизация получила список до закрытия чата, а записывает его после: закрытый чат не возвращается
// в список, для него не создаётся и не публикуется новый секрет, и ложного «закрыт собеседником» нет.
func TestCloseChatDuringSync(t *testing.T) {
	srv := apitest.NewServer()
	a, hook := hookedParty(t, srv, "alice")
	b := registered(t, srv, "bob")
	makeContacts(t, b, a)
	id := serverChat(t, b, "alice", rpcclient.ChatParams{Algorithm: cipher.RC6, KeyBits: 128, Mode: 2, Padding: 3})
	eventually(t, 2*time.Second, "чат у alice", func() bool { _, ok := findChat(a, id); return ok })
	eventually(t, 2*time.Second, "значение bob", func() bool { return srv.Value(id, "bob") != nil })

	listed, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	armed.Store(true)
	hook.set(func(list []rpcclient.Chat) []rpcclient.Chat {
		for _, c := range list {
			if c.ID == id && c.Self.Connected && armed.CompareAndSwap(true, false) {
				// Список уже получен; закрытие проходит до того, как синхронизация его запишет.
				close(listed)
				<-release
			}
		}
		return list
	})
	a.api.ResetCalls()
	// alice подключается к чату на сервере: следующий список покажет её подключённой без значения.
	if _, err := a.api.ConnectChat(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-listed:
	case <-time.After(3 * time.Second):
		t.Fatal("синхронизация не получила список")
	}
	closed := make(chan error, 1)
	go func() { closed <- a.svc.CloseChat(hex.EncodeToString(id[:])) }()
	eventually(t, 2*time.Second, "чат убран с устройства", func() bool { _, ok := findChat(a, id); return !ok })
	close(release)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	hook.set(nil)
	waitCycles(t, a, 5)

	if _, ok := findChat(a, id); ok {
		t.Fatal("закрытый чат вернулся в список")
	}
	if n := a.rec.notices("CHAT_CLOSED_BY_PEER"); len(n) != 0 {
		t.Fatalf("ложное уведомление о закрытии собеседником: %+v", n)
	}
	if n := a.api.Count("PublishPublicValue"); n != 0 {
		t.Fatalf("для закрытого чата опубликовано значение: %d", n)
	}
	db := a.svc.sess.db
	if _, ok, _ := db.DHSecret(id); ok {
		t.Fatal("для закрытого чата остался секрет")
	}
	if c, ok, _ := db.Chat(id); !ok || c.ClosedAt.IsZero() {
		t.Fatal("чат не отмечен закрытым")
	}
}

// Сбой записи чата в базу профиля: одно понятное уведомление на серию сбоев, после успешного цикла
// следующий сбой снова сообщается.
func TestStorageFailureNoticeOncePerSeries(t *testing.T) {
	srv := apitest.NewServer()
	a, hook := hookedParty(t, srv, "alice")
	// Чат с недопустимой длиной ключа нарушает ограничение таблицы: запись в базу не проходит.
	broken := rpcclient.Chat{ID: [16]byte{0xEE, 1}, Params: rpcclient.ChatParams{Algorithm: cipher.RC6, KeyBits: 100, Mode: 2, Padding: 3},
		Creator: "alice", Self: rpcclient.Participant{Login: "alice"}, Peer: rpcclient.Participant{Login: "bob"}, CreatedAt: time.Now()}
	var bad atomic.Bool
	hook.set(func(list []rpcclient.Chat) []rpcclient.Chat {
		if bad.Load() {
			return append(list, broken)
		}
		return list
	})
	dir := a.svc.sess.db.Dir()
	check := func(want int) {
		t.Helper()
		n := a.rec.notices(apperr.ProfileStorage)
		if len(n) != want {
			t.Fatalf("уведомлений %d, ожидалось %d: %+v", len(n), want, n)
		}
		for _, x := range n {
			if x.Kind != NoticeError || x.Message == apperr.ProfileStorage || !strings.Contains(x.Message, dir) {
				t.Fatalf("уведомление без понятного сообщения: %+v", x)
			}
		}
	}
	bad.Store(true)
	waitCycles(t, a, 5)
	check(1)
	bad.Store(false)
	waitCycles(t, a, 3)
	check(1)
	bad.Store(true)
	waitCycles(t, a, 5)
	check(2)
}

// Ошибка хранилища при согласовании ключа даёт одно уведомление PROFILE_STORAGE, а не молчание.
func TestAdvanceStorageErrorNotice(t *testing.T) {
	p := newParty(t, apitest.NewServer(), t.TempDir())
	path := filepath.Join(t.TempDir(), "client.db")
	db, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	id := [16]byte{0xAB}
	sess := &session{login: "alice", db: db, api: p.api, ctx: ctx, chats: map[[16]byte]*chatState{id: {}}}
	c := rpcclient.Chat{ID: id, Params: rpcclient.ChatParams{Algorithm: cipher.RC6, KeyBits: 128, Mode: 2, Padding: 3},
		Self: rpcclient.Participant{Login: "alice", Connected: true}, Peer: rpcclient.Participant{Login: "bob"}}
	for i := 0; i < 3; i++ {
		p.svc.advance(sess, c)
	}
	n := p.rec.notices(apperr.ProfileStorage)
	if len(n) != 1 || !strings.Contains(n[0].Message, filepath.Dir(path)) {
		t.Fatalf("уведомления: %+v", p.rec.notices(""))
	}
}

// Недопустимое значение собеседника: одно уведомление с именем собеседника, не каждый цикл.
func TestPeerValueInvalidNoticeOnce(t *testing.T) {
	srv, a, _ := pairWithContacts(t)
	id := serverChat(t, a, "bob", rpcclient.ChatParams{Algorithm: cipher.RC6, KeyBits: 128, Mode: 2, Padding: 3})
	srv.SetValue(id, "bob", bytes.Repeat([]byte{0xFF}, 256))
	eventually(t, 3*time.Second, "уведомление", func() bool { return len(a.rec.notices(apperr.PeerValueInvalid)) > 0 })
	waitCycles(t, a, 5)
	n := a.rec.notices(apperr.PeerValueInvalid)
	if len(n) != 1 || n[0].Kind != NoticeError || !strings.Contains(n[0].Message, "bob") {
		t.Fatalf("уведомления: %+v", n)
	}
	if c, _ := findChat(a, id); c.KeyState != "PEER_VALUE_INVALID" {
		t.Fatalf("состояние ключа %s", c.KeyState)
	}
}
