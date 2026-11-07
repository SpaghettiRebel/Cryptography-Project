package rpcclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	apiv1 "cryptochat/client/internal/api/v1"
	"cryptochat/client/internal/apperr"
)

// selfSigned создаёт сертификат для указанных имён и записывает его PEM в файл.
func selfSigned(t *testing.T, names ...string) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: names[0]},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "server.crt")
	if err := os.WriteFile(file, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return pair, file
}

type fakeAccounts struct {
	apiv1.UnimplementedAccountsServer
	auth     chan string
	deadline chan time.Duration
}

func (f *fakeAccounts) Logout(ctx context.Context, _ *apiv1.LogoutRequest) (*apiv1.LogoutResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	v := md.Get("authorization")
	if len(v) == 0 {
		f.auth <- ""
	} else {
		f.auth <- v[0]
	}
	if d, ok := ctx.Deadline(); ok {
		f.deadline <- time.Until(d)
	}
	return &apiv1.LogoutResponse{}, nil
}

func (f *fakeAccounts) Login(ctx context.Context, r *apiv1.LoginRequest) (*apiv1.LoginResponse, error) {
	if d, ok := ctx.Deadline(); ok {
		f.deadline <- time.Until(d)
	}
	if r.GetPassword() == "slow" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if r.GetPassword() != "password1" {
		st, _ := status.New(codes.Unauthenticated, "нет").WithDetails(&errdetails.ErrorInfo{Reason: "INVALID_CREDENTIALS", Domain: "cryptochat"})
		return nil, st.Err()
	}
	return &apiv1.LoginResponse{Token: "tok", UserId: "u1", Login: r.GetLogin()}, nil
}

func startServer(t *testing.T, pair tls.Certificate) (string, *fakeAccounts) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13})))
	f := &fakeAccounts{auth: make(chan string, 4), deadline: make(chan time.Duration, 4)}
	apiv1.RegisterAccountsServer(srv, f)
	h := health.NewServer()
	healthpb.RegisterHealthServer(srv, h)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	_, port, _ := net.SplitHostPort(lis.Addr().String())
	return "localhost:" + port, f
}

func dial(t *testing.T, addr, cert string) *Conn {
	t.Helper()
	c, err := Dial(addr, cert)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestSessionHeaderAndDeadlines(t *testing.T) {
	pair, cert := selfSigned(t, "localhost", "127.0.0.1")
	addr, f := startServer(t, pair)
	c := dial(t, addr, cert)
	ctx := context.Background()

	if err := c.Health(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}
	if err := c.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-f.auth; got != "" {
		t.Fatalf("заголовок без сессии: %q", got)
	}
	if d := <-f.deadline; d > CallTimeout || d < CallTimeout-time.Second {
		t.Fatalf("срок обычного вызова %v", d)
	}
	s, err := c.Login(ctx, "alice", "password1")
	if err != nil || s.Token != "tok" || s.UserID != "u1" {
		t.Fatalf("%+v %v", s, err)
	}
	if d := <-f.deadline; d > LoginTimeout || d < LoginTimeout-time.Second {
		t.Fatalf("срок входа %v", d)
	}
	c.SetToken(s.Token)
	if err := c.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	<-f.deadline
	if got := <-f.auth; got != "Bearer tok" {
		t.Fatalf("заголовок сессии %q", got)
	}
	if _, err := c.Login(ctx, "alice", "wrong-pass"); apperr.CodeOf(err) != apperr.InvalidCredentials {
		t.Fatalf("причина сервера: %v", err)
	}
	<-f.deadline
	if _, err := c.ListChats(ctx); apperr.CodeOf(err) != apperr.Internal {
		t.Fatalf("неизвестный метод: %v", err)
	}
}

func TestTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("ждёт срок вызова 10 с")
	}
	pair, cert := selfSigned(t, "localhost")
	addr, _ := startServer(t, pair)
	c := dial(t, addr, cert)
	start := time.Now()
	_, err := c.Login(context.Background(), "alice", "slow")
	if apperr.CodeOf(err) != apperr.Timeout {
		t.Fatalf("%v", err)
	}
	if d := time.Since(start); d > LoginTimeout+2*time.Second {
		t.Fatalf("ожидание %v", d)
	}
}

func TestWrongCertificate(t *testing.T) {
	pair, _ := selfSigned(t, "localhost", "127.0.0.1")
	addr, _ := startServer(t, pair)
	_, otherCert := selfSigned(t, "localhost")
	c := dial(t, addr, otherCert)
	if err := c.Health(context.Background()); apperr.CodeOf(err) != apperr.CertInvalid {
		t.Fatalf("чужой сертификат: %v", err)
	}
}

func TestWrongHostName(t *testing.T) {
	pair, cert := selfSigned(t, "server.example")
	addr, _ := startServer(t, pair)
	c := dial(t, addr, cert)
	if err := c.Health(context.Background()); apperr.CodeOf(err) != apperr.CertInvalid {
		t.Fatalf("сертификат на другое имя: %v", err)
	}
}

func TestClosedPort(t *testing.T) {
	_, cert := selfSigned(t, "localhost")
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(lis.Addr().String())
	_ = lis.Close()
	c := dial(t, "localhost:"+port, cert)
	start := time.Now()
	if err := c.Health(context.Background()); apperr.CodeOf(err) != apperr.ServerUnavailable {
		t.Fatalf("закрытый порт: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("сообщение через %v", d)
	}
}

func TestCertificateFile(t *testing.T) {
	if _, err := Dial("localhost:8443", filepath.Join(t.TempDir(), "нет.crt")); apperr.CodeOf(err) != apperr.CertFile {
		t.Fatalf("нет файла: %v", err)
	}
	if _, err := Dial("localhost:8443", ""); apperr.CodeOf(err) != apperr.CertFile {
		t.Fatalf("пустой путь: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.crt")
	_ = os.WriteFile(bad, []byte("не сертификат"), 0o600)
	if _, err := Dial("localhost:8443", bad); apperr.CodeOf(err) != apperr.CertFile {
		t.Fatalf("не PEM: %v", err)
	}
	_, good := selfSigned(t, "localhost")
	if _, err := Dial("localhost", good); apperr.CodeOf(err) != apperr.InvalidAddress {
		t.Fatalf("адрес без порта: %v", err)
	}
}

// Сервер вернулся во время паузы между попытками подключения: вызов не ждёт конца паузы и проходит.
func TestCallAfterServerReturns(t *testing.T) {
	if testing.Short() {
		t.Skip("ждёт несколько попыток подключения")
	}
	pair, cert := selfSigned(t, "localhost", "127.0.0.1")
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()
	_, port, _ := net.SplitHostPort(addr)
	c := dial(t, "localhost:"+port, cert)
	for i := 0; i < 3; i++ {
		if err := c.Health(context.Background()); apperr.CodeOf(err) != apperr.ServerUnavailable {
			t.Fatalf("сервер ещё не запущен: %v", err)
		}
	}
	lis, err = net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("порт занят: %v", err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13})))
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("первый вызов после возврата сервера: %v", err)
	}
}
