package rpc

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	apiv1 "cryptochat/server/api/v1"
	"cryptochat/server/internal/accounts"
	"cryptochat/server/internal/bootstrap"
	"cryptochat/server/internal/chats"
	"cryptochat/server/internal/contacts"
	"cryptochat/server/internal/keyexchange"
	"cryptochat/server/internal/store/memstore"
)

var bg = context.Background()

// syncBuffer — журнал, безопасный при одновременной записи.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type harness struct {
	accounts apiv1.AccountsClient
	contacts apiv1.ContactsClient
	chats    apiv1.ChatsClient
	keys     apiv1.KeyExchangeClient
	conn     *grpc.ClientConn
	log      *syncBuffer
	server   *Server
	store    *memstore.Store
}

// start поднимает настоящий gRPC с TLS поверх соединения в памяти.
func start(t *testing.T, ready func(context.Context) error, extra ...func(*Deps)) *harness {
	t.Helper()
	secrets, certs := t.TempDir(), t.TempDir()
	if err := bootstrap.Init(secrets, certs, -1); err != nil {
		t.Fatal(err)
	}
	serverTLS, err := bootstrap.ServerTLS(filepath.Join(secrets, bootstrap.ServerCertFile), filepath.Join(secrets, bootstrap.ServerKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	clientTLS, err := bootstrap.ClientTLS(filepath.Join(certs, bootstrap.ServerCertFile), "localhost")
	if err != nil {
		t.Fatal(err)
	}
	m := memstore.New()
	acc, err := accounts.New(bg, m)
	if err != nil {
		t.Fatal(err)
	}
	log := &syncBuffer{}
	deps := Deps{
		Accounts:    acc,
		Contacts:    contacts.New(m),
		Chats:       chats.New(m),
		KeyExchange: keyexchange.New(m),
		Logger:      slog.New(slog.NewTextHandler(log, nil)),
		Ready:       ready,
	}
	for _, fn := range extra {
		fn(&deps)
	}
	s := NewServer(serverTLS, deps)
	lis := bufconn.Listen(1 << 20)
	go func() { _ = s.GRPC.Serve(lis) }()
	t.Cleanup(s.GRPC.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &harness{accounts: apiv1.NewAccountsClient(conn), contacts: apiv1.NewContactsClient(conn), chats: apiv1.NewChatsClient(conn), keys: apiv1.NewKeyExchangeClient(conn), conn: conn, log: log, server: s, store: m}
}

func auth(token string) context.Context {
	return metadata.AppendToOutgoingContext(bg, "authorization", "Bearer "+token)
}

func (h *harness) signUp(t *testing.T, login string) (string, string) {
	t.Helper()
	const pw = "password-for-" + "tests"
	if _, err := h.accounts.Register(bg, &apiv1.RegisterRequest{Login: login, Password: pw}); err != nil {
		t.Fatalf("Register(%s): %v", login, err)
	}
	r, err := h.accounts.Login(bg, &apiv1.LoginRequest{Login: login, Password: pw})
	if err != nil {
		t.Fatalf("Login(%s): %v", login, err)
	}
	return r.GetToken(), r.GetUserId()
}
