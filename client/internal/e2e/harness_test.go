//go:build e2e

// Пакет e2e проверяет сервисный слой клиента на настоящем сервере из deploy/compose.yaml:
// go test -tags e2e ./internal/e2e -v -timeout 20m (из папки client).
// Все тесты пакета работают с одним проектом Compose; он поднимается в TestMain и удаляется в конце.
package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"

	apiv1 "cryptochat/client/internal/api/v1"
	"cryptochat/client/internal/localdb"
	"cryptochat/client/internal/profile"
	"cryptochat/client/internal/rpcclient"
	"cryptochat/client/internal/service"
)

const (
	project = "cryptochat-client-e2e"
	port    = "18445"
	address = "localhost:" + port
)

var (
	root     string // папка project
	compose  string // deploy/compose.yaml
	certsDir string
	certFile string
)

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		code = 1
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	var err error
	if root, err = filepath.Abs("../../.."); err != nil {
		return 1, err
	}
	compose = filepath.Join(root, "deploy", "compose.yaml")
	if certsDir, err = os.MkdirTemp("", "cryptochat-client-e2e-"); err != nil {
		return 1, err
	}
	defer os.RemoveAll(certsDir)
	certFile = filepath.Join(certsDir, "server.crt")
	_, _ = runCompose("down", "-v", "--remove-orphans")
	defer func() {
		if out, err := runCompose("down", "-v", "--remove-orphans"); err != nil {
			fmt.Fprintf(os.Stderr, "уборка Compose: %v\n%s", err, out)
		}
	}()
	if out, err := runCompose("up", "--build", "-d"); err != nil {
		return 1, fmt.Errorf("up: %v\n%s", err, out)
	}
	if err := waitHealthy(2 * time.Minute); err != nil {
		logs, _ := runCompose("logs", "--no-color", "--tail", "100")
		return 1, fmt.Errorf("%v\n%s", err, logs)
	}
	code := m.Run()
	if code != 0 {
		logs, _ := runCompose("logs", "--no-color", "--tail", "200", "server")
		fmt.Fprintf(os.Stderr, "журнал сервера:\n%s\n", logs)
	}
	return code, nil
}

func runCompose(args ...string) (string, error) {
	cmd := exec.Command("docker", append([]string{"compose", "-p", project, "-f", compose}, args...)...)
	cmd.Env = append(os.Environ(), "CHAT_PUBLIC_PORT="+port, "CHAT_CERTS_DIR="+certsDir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustCompose(t *testing.T, args ...string) string {
	t.Helper()
	out, err := runCompose(args...)
	if err != nil {
		t.Fatalf("docker compose %v: %v\n%s", args, err, out)
	}
	return out
}

func dialRaw() (*grpc.ClientConn, error) {
	pem, err := os.ReadFile(certFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("в файле нет сертификата")
	}
	return grpc.NewClient(address, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS13,
	})))
}

func waitHealthy(limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(certFile); err == nil {
			conn, err := dialRaw()
			if err == nil {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				r, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
				cancel()
				_ = conn.Close()
				if err == nil && r.GetStatus() == healthpb.HealthCheckResponse_SERVING {
					return nil
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("сервер не стал готов за %v", limit)
}

// stopServer останавливает только сервер; база и брокер продолжают работать.
func stopServer(t *testing.T) {
	t.Helper()
	mustCompose(t, "stop", "server")
}

// startServer запускает сервер и ждёт готовности.
func startServer(t *testing.T) {
	t.Helper()
	mustCompose(t, "start", "server")
	if err := waitHealthy(2 * time.Minute); err != nil {
		t.Fatal(err)
	}
}

func serverLogs(t *testing.T) string {
	t.Helper()
	return mustCompose(t, "logs", "--no-color", "server")
}

func pgDump(t *testing.T) string {
	t.Helper()
	return mustCompose(t, "exec", "-T", "postgres", "pg_dump", "-U", "cryptochat", "cryptochat")
}

// recorder собирает события сервиса.
type recorder struct {
	mu     sync.Mutex
	events []event
}

type event struct {
	name string
	data any
	at   time.Time
}

func (r *recorder) Emit(name string, data any) {
	r.mu.Lock()
	r.events = append(r.events, event{name, data, time.Now()})
	r.mu.Unlock()
}

func (r *recorder) all() []event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]event(nil), r.events...)
}

func (r *recorder) notices(code string) []service.Notice {
	var out []service.Notice
	for _, e := range r.all() {
		if n, ok := e.data.(service.Notice); ok && n.Code == code {
			out = append(out, n)
		}
	}
	return out
}

func (r *recorder) ended() []service.SessionEnded {
	var out []service.SessionEnded
	for _, e := range r.all() {
		if v, ok := e.data.(service.SessionEnded); ok {
			out = append(out, v)
		}
	}
	return out
}

// tokenSpy запоминает токен последнего входа, чтобы тест мог отозвать ту же сессию.
type tokenSpy struct {
	rpcclient.API
	owner   *participant
	mu      sync.Mutex
	token   string
	publish int
	peer    int
}

// Секретные значения и ключи всех участников за прогон: SC-004 ищет их на сервере и в интерфейсе.
var (
	secretsMu sync.Mutex
	secrets   [][]byte
)

func rememberSecret(v []byte) {
	secretsMu.Lock()
	secrets = append(secrets, append([]byte(nil), v...))
	secretsMu.Unlock()
}

func allSecrets() [][]byte {
	secretsMu.Lock()
	defer secretsMu.Unlock()
	return append([][]byte(nil), secrets...)
}

func (s *tokenSpy) Login(ctx context.Context, login, password string) (rpcclient.Session, error) {
	r, err := s.API.Login(ctx, login, password)
	if err == nil {
		s.mu.Lock()
		s.token = r.Token
		s.mu.Unlock()
	}
	return r, err
}

func (s *tokenSpy) PublishPublicValue(ctx context.Context, id [16]byte, v []byte) error {
	s.mu.Lock()
	s.publish++
	s.mu.Unlock()
	// Секрет записан до публикации: запоминаем его для поиска в журналах и данных сервера.
	if db := s.owner.store(); db != nil {
		if sec, ok, err := db.DHSecret(id); err == nil && ok {
			rememberSecret(sec.Private)
		}
	}
	return s.API.PublishPublicValue(ctx, id, v)
}

func (s *tokenSpy) GetPeerPublicValue(ctx context.Context, id [16]byte) ([]byte, bool, error) {
	s.mu.Lock()
	s.peer++
	s.mu.Unlock()
	return s.API.GetPeerPublicValue(ctx, id)
}

// publishCalls — число публикаций открытого значения с момента запуска сервиса.
func (s *tokenSpy) publishCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publish
}

// exchangeCalls — все обращения к обмену значениями с момента запуска сервиса.
func (s *tokenSpy) exchangeCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publish + s.peer
}

func (s *tokenSpy) lastToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

// participant — отдельная копия клиента со своей папкой данных.
type participant struct {
	t        *testing.T
	login    string
	password string
	home     string
	svc      *service.Service
	rec      *recorder
	spy      *tokenSpy

	dbMu sync.Mutex
	db   *localdb.DB // открытая сервисом база профиля
}

func (p *participant) store() *localdb.DB {
	p.dbMu.Lock()
	defer p.dbMu.Unlock()
	return p.db
}

func randomLogin(prefix string) string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// newParticipant создаёт пользователя с новой папкой данных; регистрация — отдельно.
func newParticipant(t *testing.T, prefix string) *participant {
	t.Helper()
	p := &participant{t: t, login: randomLogin(prefix), password: "e2e-password-" + prefix, home: t.TempDir()}
	if err := profile.Save(p.home, profile.Settings{ServerAddress: address, CertificatePath: certFile}); err != nil {
		t.Fatal(err)
	}
	p.open()
	return p
}

// open запускает сервис с папкой данных участника (как запуск приложения).
func (p *participant) open() {
	p.t.Helper()
	p.rec = &recorder{}
	p.spy = nil
	p.svc = service.New(service.Config{
		Root: p.home,
		Dial: func(addr, cert string) (rpcclient.API, error) {
			c, err := rpcclient.Dial(addr, cert)
			if err != nil {
				return nil, err
			}
			p.spy = &tokenSpy{API: c, owner: p}
			return p.spy, nil
		},
		OpenStore: func(path string) (*localdb.DB, error) {
			db, err := localdb.Open(path)
			if err == nil {
				p.dbMu.Lock()
				p.db = db
				p.dbMu.Unlock()
			}
			return db, err
		},
		Logger: log.New(io.Discard, "", 0),
	})
	p.svc.Subscribe(p.rec)
	svc := p.svc
	p.t.Cleanup(svc.Close)
}

// restart закрывает сервис и открывает снова с той же папкой данных.
func (p *participant) restart() {
	p.t.Helper()
	p.svc.Close()
	p.open()
}

func (p *participant) register() *participant {
	p.t.Helper()
	if err := p.svc.Register(p.login, p.password); err != nil {
		p.t.Fatalf("регистрация %s: %v", p.login, err)
	}
	return p
}

func (p *participant) signIn() *participant {
	p.t.Helper()
	if _, err := p.svc.Login(p.login, p.password); err != nil {
		p.t.Fatalf("вход %s: %v", p.login, err)
	}
	return p
}

// user регистрирует нового участника и входит.
func user(t *testing.T, prefix string) *participant {
	t.Helper()
	return newParticipant(t, prefix).register().signIn()
}

func befriend(t *testing.T, a, b *participant) {
	t.Helper()
	if _, err := a.svc.SendContactRequest(b.login); err != nil {
		t.Fatal(err)
	}
	if err := b.svc.AcceptContact(a.login); err != nil {
		t.Fatal(err)
	}
}

func (p *participant) chat(id [16]byte) (service.ChatInfo, bool) {
	for _, c := range p.svc.Snapshot().Chats {
		if c.ID == id {
			return c, true
		}
	}
	return service.ChatInfo{}, false
}

// eventually ждёт условия и возвращает время ожидания.
func eventually(t *testing.T, limit time.Duration, what string, cond func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for !cond() {
		if time.Since(start) > limit {
			t.Fatalf("не дождались за %v: %s", limit, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return time.Since(start)
}

// raw — клиент сервера напрямую для действий, которых нет в сервисе.
type raw struct {
	t     *testing.T
	conn  *grpc.ClientConn
	token string
}

func rawClient(t *testing.T, token string) *raw {
	t.Helper()
	conn, err := dialRaw()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &raw{t: t, conn: conn, token: token}
}

func (r *raw) ctx() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+r.token), cancel
}

// login входит отдельной сессией (другой токен, тот же пользователь).
func rawLogin(t *testing.T, login, password string) *raw {
	t.Helper()
	r := rawClient(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := apiv1.NewAccountsClient(r.conn).Login(ctx, &apiv1.LoginRequest{Login: login, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	r.token = resp.GetToken()
	return r
}

func (r *raw) createChat(peer string, alg apiv1.Algorithm, bits int, mode apiv1.CipherMode, pad apiv1.PaddingMode) [16]byte {
	r.t.Helper()
	ctx, cancel := r.ctx()
	defer cancel()
	resp, err := apiv1.NewChatsClient(r.conn).CreateChat(ctx, &apiv1.CreateChatRequest{PeerLogin: peer, Params: &apiv1.ChatParams{
		Algorithm: alg, KeyBits: uint32(bits), CipherMode: mode, PaddingMode: pad}})
	if err != nil {
		r.t.Fatal(err)
	}
	var id [16]byte
	copy(id[:], resp.GetChat().GetChatId())
	return id
}

func (r *raw) getChat(id [16]byte) (*apiv1.Chat, error) {
	ctx, cancel := r.ctx()
	defer cancel()
	resp, err := apiv1.NewChatsClient(r.conn).GetChat(ctx, &apiv1.ChatRef{ChatId: id[:]})
	return resp.GetChat(), err
}

func (r *raw) peerValue(id [16]byte) []byte {
	r.t.Helper()
	ctx, cancel := r.ctx()
	defer cancel()
	resp, err := apiv1.NewKeyExchangeClient(r.conn).GetPeerPublicValue(ctx, &apiv1.ChatRef{ChatId: id[:]})
	if err != nil {
		r.t.Fatal(err)
	}
	return resp.GetPublicValue()
}

func (r *raw) logout() {
	r.t.Helper()
	ctx, cancel := r.ctx()
	defer cancel()
	if _, err := apiv1.NewAccountsClient(r.conn).Logout(ctx, &apiv1.LogoutRequest{}); err != nil {
		r.t.Fatal(err)
	}
}

// foreignCert — самоподписанный сертификат на localhost, которому сервер не соответствует.
func foreignCert(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
