package delivery

import (
	"bytes"
	"context"
	"io"
	"log"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cryptochat/client/internal/apitest"
	"cryptochat/client/internal/keyagree"
	"cryptochat/client/internal/localdb"
	"cryptochat/client/internal/rpcclient"
	"cryptochat/core/cipher"
)

// events собирает события доставки.
type events struct {
	mu       sync.Mutex
	messages []MessageView
	progress []ProgressEvent
	times    []time.Time // время каждого события progress
	notices  []string
	closed   int
	expired  int
	onRecv   func([16]byte) // вызывается при каждом изменении состояния приёма
}

func (e *events) Messages(_ [16]byte, changed []MessageView) {
	e.mu.Lock()
	e.messages = append(e.messages, changed...)
	e.mu.Unlock()
}
func (e *events) Progress(p ProgressEvent) {
	e.mu.Lock()
	e.progress = append(e.progress, p)
	e.times = append(e.times, time.Now())
	e.mu.Unlock()
}
func (e *events) Notice(_, code, _ string) {
	e.mu.Lock()
	e.notices = append(e.notices, code)
	e.mu.Unlock()
}
func (e *events) ReceiveChanged(chat [16]byte) {
	e.mu.Lock()
	f := e.onRecv
	e.mu.Unlock()
	if f != nil {
		f(chat)
	}
}
func (e *events) ChatClosed([16]byte) { e.mu.Lock(); e.closed++; e.mu.Unlock() }
func (e *events) SessionExpired()     { e.mu.Lock(); e.expired++; e.mu.Unlock() }

func (e *events) noticeCount(code string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, c := range e.notices {
		if c == code {
			n++
		}
	}
	return n
}

func (e *events) progressOf(seq int64, stage Stage) []ProgressEvent {
	out, _ := e.progressTimes(seq, stage)
	return out
}

func (e *events) progressTimes(seq int64, stage Stage) ([]ProgressEvent, []time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var (
		out   []ProgressEvent
		times []time.Time
	)
	for i, p := range e.progress {
		if p.Seq == seq && p.Stage == stage {
			out = append(out, p)
			times = append(times, e.times[i])
		}
	}
	return out, times
}

// side — один участник: своя база, своё соединение с подделкой сервера, своя доставка.
type side struct {
	t     *testing.T
	login string
	path  string
	api   *apitest.Client
	db    *localdb.DB
	store Store
	ev    *events
	e     *Engine
	chat  ChatInfo

	mu     sync.Mutex
	key    keyagree.State
	active bool
	extra  map[[16]byte]ChatInfo // другие чаты участника
	waits  []time.Duration
	fast   bool // паузы повторов не ждут
}

// pair — два участника с общим чатом и ключом в состоянии READY на подделке сервера.
type pair struct {
	srv   *apitest.Server
	a, b  *side
	id    [16]byte
	alg   cipher.Algorithm
	mode  int
	pad   int
	bits  int
	setup func(*side)
}

func newPair(t *testing.T, mode, pad, bits int) *pair {
	t.Helper()
	return newPairAlg(t, cipher.RC6, mode, pad, bits)
}

// newPairAlg — пара с чатом на алгоритме alg (в сборке с devcipher — тестовая замена).
func newPairAlg(t *testing.T, alg cipher.Algorithm, mode, pad, bits int) *pair {
	t.Helper()
	srv := apitest.NewServer()
	ctx := context.Background()
	ca, cb := srv.Client(), srv.Client()
	for _, l := range []struct {
		c *apitest.Client
		n string
	}{{ca, "alice"}, {cb, "bob"}} {
		if err := l.c.Register(ctx, l.n, "password-123"); err != nil {
			t.Fatal(err)
		}
		s, err := l.c.Login(ctx, l.n, "password-123")
		if err != nil {
			t.Fatal(err)
		}
		l.c.SetToken(s.Token)
	}
	if _, err := ca.SendContactRequest(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := cb.AcceptContact(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	c, err := ca.CreateChat(ctx, "bob", rpcclient.ChatParams{Algorithm: alg, KeyBits: bits, Mode: mode, Padding: pad})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cb.ConnectChat(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	for _, x := range []*apitest.Client{ca, cb} {
		if err := x.PublishPublicValue(ctx, c.ID, bytes.Repeat([]byte{1}, 256)); err != nil {
			t.Fatal(err)
		}
	}
	p := &pair{srv: srv, id: c.ID, alg: alg, mode: mode, pad: pad, bits: bits}
	key := bytes.Repeat([]byte{0x5A}, bits/8)
	p.a = newSide(t, "alice", ca, p, key)
	p.b = newSide(t, "bob", cb, p, key)
	return p
}

func newSide(t *testing.T, login string, api *apitest.Client, p *pair, key []byte) *side {
	t.Helper()
	s := &side{t: t, login: login, path: filepath.Join(t.TempDir(), "client.db"), api: api, key: keyagree.Ready, active: true,
		chat: ChatInfo{ID: p.id, Algorithm: p.alg, KeyBits: p.bits, Mode: p.mode, Padding: p.pad}}
	db, err := localdb.Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertChat(localdb.Chat{ID: p.id, Peer: "x", Creator: "alice", Algorithm: p.alg, KeyBits: p.bits,
		Mode: p.mode, Padding: p.pad, CreatedAt: time.UnixMilli(1)}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSessionKeyAndDropSecret(p.id, key); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	s.open()
	return s
}

// open открывает базу и запускает доставку (как вход в приложение).
func (s *side) open() {
	s.t.Helper()
	db, err := localdb.Open(s.path)
	if err != nil {
		s.t.Fatal(err)
	}
	s.db = db
	if w, ok := s.store.(*faultStore); ok {
		w.Store = db
	} else {
		s.store = db
	}
	s.ev = &events{}
	s.e = New(Config{
		API: s.api, Store: s.store, Events: s.ev, Logger: log.New(io.Discard, "", 0),
		Chat: func(id [16]byte) (ChatInfo, bool) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if c, ok := s.extra[id]; ok {
				c.KeyState = keyagree.Ready
				return c, true
			}
			c := s.chat
			c.KeyState = s.key
			return c, s.active && id == c.ID
		},
		Wait: func(ctx context.Context, d time.Duration) bool {
			s.mu.Lock()
			s.waits = append(s.waits, d)
			fast := s.fast
			s.mu.Unlock()
			if fast {
				d = 5 * time.Millisecond
			}
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return false
			case <-t.C:
				return true
			}
		},
	})
	s.e.Start()
	s.t.Cleanup(s.close)
}

// close останавливает доставку и закрывает базу (как выход из приложения).
func (s *side) close() {
	if s.e != nil {
		s.e.Stop()
		s.e = nil
	}
	if s.db != nil {
		_ = s.db.Close()
		s.db = nil
	}
}

func (s *side) restart() {
	s.t.Helper()
	s.close()
	s.open()
}

func (s *side) receive() {
	s.t.Helper()
	if err := s.e.OpenReceiver(s.chat.ID); err != nil {
		s.t.Fatal(err)
	}
	eventually(s.t, 3*time.Second, "приём открыт", func() bool { st, _ := s.e.ReceiveState(s.chat.ID); return st == RecvReceiving })
}

func (s *side) msg(seq int64) localdb.Message {
	s.t.Helper()
	m, ok, err := s.db.Message(seq)
	if err != nil || !ok {
		s.t.Fatalf("нет сообщения %d: %v", seq, err)
	}
	return m
}

// incoming возвращает входящие сообщения (без служебных).
func (s *side) incoming() []localdb.Message {
	list, _, err := s.db.ListMessages(s.chat.ID, 0, 200)
	if err != nil {
		s.t.Fatal(err)
	}
	var out []localdb.Message
	for _, m := range list {
		if m.Direction == localdb.DirIn {
			out = append(out, m)
		}
	}
	return out
}

func (s *side) waitState(seq int64, d time.Duration, states ...int) localdb.Message {
	s.t.Helper()
	var m localdb.Message
	eventually(s.t, d, "состояние сообщения", func() bool {
		m = s.msg(seq)
		for _, st := range states {
			if m.State == st {
				return true
			}
		}
		return false
	})
	return m
}

// waitIncoming ждёт n входящих в одном из состояний.
func (s *side) waitIncoming(n int, d time.Duration, state int) []localdb.Message {
	s.t.Helper()
	var list []localdb.Message
	eventually(s.t, d, "входящие", func() bool {
		list = s.incoming()
		c := 0
		for _, m := range list {
			if m.State == state {
				c++
			}
		}
		return c >= n
	})
	return list
}

// content читает содержимое файла сообщения из кусков.
func (s *side) content(seq int64) []byte {
	s.t.Helper()
	var out bytes.Buffer
	after := -1
	for {
		list, err := s.db.Chunks(seq, after, 16)
		if err != nil {
			s.t.Fatal(err)
		}
		if len(list) == 0 {
			return out.Bytes()
		}
		for _, c := range list {
			out.Write(c.Data)
		}
		after = list[len(list)-1].Idx
	}
}

func eventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("не дождались за %v: %s", d, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// faultStore — хранилище с подменяемыми ошибками записи.
type faultStore struct {
	Store
	mu         sync.Mutex
	failBatch  int // сколько следующих ReceiveBatch завершить ошибкой
	panicBatch int // сколько следующих ReceiveBatch завершить паникой
	failAdd    int // сколько следующих AddOutFragment завершить ошибкой
}

func (f *faultStore) AddOutFragment(seq int64, idx int, wire []byte) (bool, error) {
	f.mu.Lock()
	fail := f.failAdd > 0
	if fail {
		f.failAdd--
	}
	f.mu.Unlock()
	if fail {
		return false, io.ErrShortWrite
	}
	return f.Store.AddOutFragment(seq, idx, wire)
}

func (f *faultStore) ReceiveBatch(chat [16]byte, in []localdb.Incoming, max uint32) ([]localdb.Received, error) {
	f.mu.Lock()
	fail, boom := f.failBatch > 0, false
	if fail {
		f.failBatch--
	} else if f.panicBatch > 0 {
		f.panicBatch--
		boom = true
	}
	f.mu.Unlock()
	if boom {
		panic("сбой обработки")
	}
	if fail {
		return nil, io.ErrShortWrite
	}
	return f.Store.ReceiveBatch(chat, in, max)
}

// withFaults перезапускает участника с хранилищем, в котором можно подменять ошибки.
func (s *side) withFaults() *faultStore {
	s.close()
	f := &faultStore{}
	s.store = f
	s.open()
	return f
}

func (s *side) setFast() {
	s.mu.Lock()
	s.fast = true
	s.mu.Unlock()
}

func (s *side) waitsCopy() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.waits...)
}

// addChat создаёт второй готовый чат пары.
func (p *pair) addChat(t *testing.T) [16]byte {
	t.Helper()
	ctx := context.Background()
	c, err := p.a.api.CreateChat(ctx, "bob", rpcclient.ChatParams{Algorithm: cipher.RC6, KeyBits: 128, Mode: 2, Padding: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.b.api.ConnectChat(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	for _, x := range []*apitest.Client{p.a.api, p.b.api} {
		if err := x.PublishPublicValue(ctx, c.ID, bytes.Repeat([]byte{1}, 256)); err != nil {
			t.Fatal(err)
		}
	}
	key := bytes.Repeat([]byte{0x33}, 16)
	for _, s := range []*side{p.a, p.b} {
		if err := s.db.UpsertChat(localdb.Chat{ID: c.ID, Peer: "x", Creator: "alice", Algorithm: cipher.RC6, KeyBits: 128,
			Mode: 2, Padding: 3, CreatedAt: time.UnixMilli(2)}); err != nil {
			t.Fatal(err)
		}
		if err := s.db.SaveSessionKeyAndDropSecret(c.ID, key); err != nil {
			t.Fatal(err)
		}
		s.mu.Lock()
		if s.extra == nil {
			s.extra = map[[16]byte]ChatInfo{}
		}
		s.extra[c.ID] = ChatInfo{ID: c.ID, Algorithm: cipher.RC6, KeyBits: 128, Mode: 2, Padding: 3}
		s.mu.Unlock()
	}
	return c.ID
}
