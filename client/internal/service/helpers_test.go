package service

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cryptochat/client/internal/apitest"
	"cryptochat/client/internal/profile"
	"cryptochat/client/internal/rpcclient"
)

// recorder собирает события сервиса.
type recorder struct {
	mu     sync.Mutex
	events []recorded
}

type recorded struct {
	name string
	data any
}

func (r *recorder) Emit(event string, data any) {
	r.mu.Lock()
	r.events = append(r.events, recorded{event, data})
	r.mu.Unlock()
}

func (r *recorder) all() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorded(nil), r.events...)
}

func (r *recorder) notices(code string) []Notice {
	var out []Notice
	for _, e := range r.all() {
		if n, ok := e.data.(Notice); ok && e.name == EventNotice && (code == "" || n.Code == code) {
			out = append(out, n)
		}
	}
	return out
}

func (r *recorder) ended() []SessionEnded {
	var out []SessionEnded
	for _, e := range r.all() {
		if v, ok := e.data.(SessionEnded); ok && e.name == EventSessionEnded {
			out = append(out, v)
		}
	}
	return out
}

func (r *recorder) snapshots() []Snapshot {
	var out []Snapshot
	for _, e := range r.all() {
		if v, ok := e.data.(Snapshot); ok && e.name == EventSnapshot {
			out = append(out, v)
		}
	}
	return out
}

// syncBuffer — журнал, безопасный для записи из горутин.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// party — участник на поддельном сервере со своей папкой данных.
type party struct {
	t    *testing.T
	svc  *Service
	api  *apitest.Client
	rec  *recorder
	logs *syncBuffer
	root string
}

const testPassword = "password-123"

func newParty(t *testing.T, srv *apitest.Server, root string) *party {
	t.Helper()
	return newPartyWith(t, srv, root, nil)
}

// newPartyWith — участник, у которого соединение с сервером обёрнуто функцией wrap (nil — без обёртки).
func newPartyWith(t *testing.T, srv *apitest.Server, root string, wrap func(*apitest.Client) rpcclient.API) *party {
	t.Helper()
	p := &party{t: t, api: srv.Client(), rec: &recorder{}, logs: &syncBuffer{}, root: root}
	// Папка уже используется — настройки остаются как есть (повторный запуск приложения).
	if _, err := os.Stat(filepath.Join(root, "settings.json")); err != nil {
		if err := profile.Save(root, profile.Settings{ServerAddress: "localhost:8443", CertificatePath: "test.crt"}); err != nil {
			t.Fatal(err)
		}
	}
	p.svc = New(Config{
		Root: root,
		Dial: func(string, string) (rpcclient.API, error) {
			if wrap != nil {
				return wrap(p.api), nil
			}
			return p.api, nil
		},
		Interval: 50 * time.Millisecond,
		Logger:   log.New(io.MultiWriter(p.logs), "", 0),
	})
	p.svc.Subscribe(p.rec)
	t.Cleanup(p.svc.Close)
	return p
}

// registered создаёт пользователя и входит.
func registered(t *testing.T, srv *apitest.Server, login string) *party {
	t.Helper()
	p := newParty(t, srv, t.TempDir())
	if err := p.svc.Register(login, testPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := p.svc.Login(login, testPassword); err != nil {
		t.Fatal(err)
	}
	return p
}

// contacts делает двух участников контактами.
func makeContacts(t *testing.T, a, b *party) {
	t.Helper()
	if _, err := a.svc.SendContactRequest(b.login()); err != nil {
		t.Fatal(err)
	}
	if err := b.svc.AcceptContact(a.login()); err != nil {
		t.Fatal(err)
	}
}

func (p *party) login() string {
	s := p.svc.Snapshot()
	if s.Session == nil {
		p.t.Fatal("нет сессии")
	}
	return s.Session.Login
}

// eventually ждёт условия до срока.
func eventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("не дождались: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if n != "" && strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// testPEM возвращает самоподписанный сертификат в PEM.
func testPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
