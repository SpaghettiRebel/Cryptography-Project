//go:build e2e

// Пакет проверяет связку криптоядра и сервера: go test -tags e2e ./... -v -timeout 15m
// Сервер не зависит от core, а core от сети, поэтому эта проверка живёт в отдельном модуле с одними тестами.
package integration

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"

	"cryptochat/core/cipher"
	"cryptochat/core/dh"
	"cryptochat/core/kdf"
	apiv1 "cryptochat/server/api/v1"
)

const (
	project = "cryptochat-integration"
	port    = "18444"
	addr    = "localhost:" + port
)

func compose(t *testing.T, file, certs string, args ...string) {
	t.Helper()
	if out, err := runCompose(file, certs, args...); err != nil {
		t.Fatalf("docker compose %v: %v\n%s", args, err, out)
	}
}

func runCompose(file, certs string, args ...string) (string, error) {
	cmd := exec.Command("docker", append([]string{"compose", "-p", project, "-f", file}, args...)...)
	cmd.Env = append(os.Environ(), "CHAT_PUBLIC_PORT="+port, "CHAT_CERTS_DIR="+certs)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func dial(t *testing.T, certFile string) *grpc.ClientConn {
	t.Helper()
	pem, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("файл не содержит сертификата")
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS13,
	})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func waitReady(t *testing.T, conn *grpc.ClientConn) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	health := healthpb.NewHealthClient(conn)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		r, err := health.Check(ctx, &healthpb.HealthCheckRequest{})
		cancel()
		if err == nil && r.GetStatus() == healthpb.HealthCheckResponse_SERVING {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("сервер не стал готов")
}

// client — сторона чата: только публичный API сервера и криптоядро.
type client struct {
	t     *testing.T
	login string
	token string
	acc   apiv1.AccountsClient
	con   apiv1.ContactsClient
	chats apiv1.ChatsClient
	keys  apiv1.KeyExchangeClient
}

func (c *client) ctx() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+c.token), cancel
}

func newClient(t *testing.T, conn *grpc.ClientConn, login string) *client {
	t.Helper()
	c := &client{t: t, login: login, acc: apiv1.NewAccountsClient(conn), con: apiv1.NewContactsClient(conn),
		chats: apiv1.NewChatsClient(conn), keys: apiv1.NewKeyExchangeClient(conn)}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	const pw = "integration-passphrase"
	if _, err := c.acc.Register(ctx, &apiv1.RegisterRequest{Login: login, Password: pw}); err != nil {
		t.Fatal(err)
	}
	r, err := c.acc.Login(ctx, &apiv1.LoginRequest{Login: login, Password: pw})
	if err != nil {
		t.Fatal(err)
	}
	c.token = r.GetToken()
	return c
}

// sessionKey выполняет сторону протокола клиента и возвращает сеансовый ключ.
func (c *client) sessionKey(pair *dh.KeyPair, group *dh.Group, chatID []byte, a cipher.Algorithm, bits int) []byte {
	c.t.Helper()
	ctx, cancel := c.ctx()
	defer cancel()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, err := c.keys.GetPeerPublicValue(ctx, &apiv1.ChatRef{ChatId: chatID})
		if err != nil {
			c.t.Fatal(err)
		}
		if r.GetPublished() {
			peer, err := group.ParsePublic(r.GetPublicValue())
			if err != nil {
				c.t.Fatalf("значение собеседника: %v", err)
			}
			shared, err := pair.SharedSecret(peer)
			if err != nil {
				c.t.Fatal(err)
			}
			var id [16]byte
			copy(id[:], chatID)
			key, err := kdf.SessionKey(shared, id, a, bits)
			if err != nil {
				c.t.Fatal(err)
			}
			return key
		}
		if time.Now().After(deadline) {
			c.t.Fatal("значение собеседника не появилось")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// SC-002: значения из криптоядра проходят через настоящий сервер, и стороны получают один ключ.
func TestKeyAgreementThroughServer(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "deploy", "compose.yaml")
	certs := t.TempDir()
	_, _ = runCompose(file, certs, "down", "-v", "--remove-orphans")
	t.Cleanup(func() { _, _ = runCompose(file, certs, "down", "-v", "--remove-orphans") })
	compose(t, file, certs, "up", "--build", "-d")

	conn := dial(t, filepath.Join(certs, "server.crt"))
	waitReady(t, conn)
	alice, bob := newClient(t, conn, "alice"), newClient(t, conn, "bob")
	func() {
		ctx, cancel := alice.ctx()
		defer cancel()
		if _, err := alice.con.SendRequest(ctx, &apiv1.SendContactRequest{Login: "bob"}); err != nil {
			t.Fatal(err)
		}
	}()
	func() {
		ctx, cancel := bob.ctx()
		defer cancel()
		if _, err := bob.con.AcceptRequest(ctx, &apiv1.ContactLogin{Login: "alice"}); err != nil {
			t.Fatal(err)
		}
	}()

	group := dh.Group14()
	cases := []struct {
		wire apiv1.Algorithm
		core cipher.Algorithm
		bits int
	}{
		{apiv1.Algorithm_ALGORITHM_MAGENTA, cipher.MAGENTA, 128},
		{apiv1.Algorithm_ALGORITHM_RC6, cipher.RC6, 128},
		{apiv1.Algorithm_ALGORITHM_MAGENTA, cipher.MAGENTA, 192},
		{apiv1.Algorithm_ALGORITHM_RC6, cipher.RC6, 192},
		{apiv1.Algorithm_ALGORITHM_MAGENTA, cipher.MAGENTA, 256},
		{apiv1.Algorithm_ALGORITHM_RC6, cipher.RC6, 256},
	}
	keys := map[string]bool{}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s-%d", tc.core, tc.bits), func(t *testing.T) {
			alice.t, bob.t = t, t
			ctx, cancel := alice.ctx()
			defer cancel()
			created, err := alice.chats.CreateChat(ctx, &apiv1.CreateChatRequest{PeerLogin: "bob", Params: &apiv1.ChatParams{
				Algorithm: tc.wire, KeyBits: uint32(tc.bits),
				CipherMode: apiv1.CipherMode_CIPHER_MODE_CBC, PaddingMode: apiv1.PaddingMode_PADDING_MODE_PKCS7,
			}})
			if err != nil {
				t.Fatal(err)
			}
			id := created.GetChat().GetChatId()
			bctx, bcancel := bob.ctx()
			defer bcancel()
			if _, err := bob.chats.ConnectChat(bctx, &apiv1.ChatRef{ChatId: id}); err != nil {
				t.Fatal(err)
			}
			pairA, err := group.GenerateKey()
			if err != nil {
				t.Fatal(err)
			}
			pairB, err := group.GenerateKey()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := alice.keys.PublishPublicValue(ctx, &apiv1.PublishPublicValueRequest{ChatId: id, PublicValue: pairA.PublicBytes()}); err != nil {
				t.Fatal(err)
			}
			if _, err := bob.keys.PublishPublicValue(bctx, &apiv1.PublishPublicValueRequest{ChatId: id, PublicValue: pairB.PublicBytes()}); err != nil {
				t.Fatal(err)
			}
			// Значения приходят байт в байт.
			got, err := bob.keys.GetPeerPublicValue(bctx, &apiv1.ChatRef{ChatId: id})
			if err != nil || !bytes.Equal(got.GetPublicValue(), pairA.PublicBytes()) {
				t.Fatalf("значение изменилось при передаче: %v", err)
			}
			keyA := alice.sessionKey(pairA, group, id, tc.core, tc.bits)
			keyB := bob.sessionKey(pairB, group, id, tc.core, tc.bits)
			if !bytes.Equal(keyA, keyB) {
				t.Fatal("сеансовые ключи различаются")
			}
			if len(keyA) != tc.bits/8 {
				t.Fatalf("длина ключа %d", len(keyA))
			}
			if keys[string(keyA)] {
				t.Fatal("ключ повторился в другом чате")
			}
			keys[string(keyA)] = true
		})
	}
}

// Сервер проверяет только длину, поэтому недопустимое значение обязан отвергнуть клиент.
func TestClientRejectsBadValues(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "deploy", "compose.yaml")
	certs := t.TempDir()
	_, _ = runCompose(file, certs, "down", "-v", "--remove-orphans")
	t.Cleanup(func() { _, _ = runCompose(file, certs, "down", "-v", "--remove-orphans") })
	compose(t, file, certs, "up", "--build", "-d")
	conn := dial(t, filepath.Join(certs, "server.crt"))
	waitReady(t, conn)
	alice, bob := newClient(t, conn, "alice"), newClient(t, conn, "bob")
	actx, acancel := alice.ctx()
	defer acancel()
	if _, err := alice.con.SendRequest(actx, &apiv1.SendContactRequest{Login: "bob"}); err != nil {
		t.Fatal(err)
	}
	bctx, bcancel := bob.ctx()
	defer bcancel()
	if _, err := bob.con.AcceptRequest(bctx, &apiv1.ContactLogin{Login: "alice"}); err != nil {
		t.Fatal(err)
	}
	group := dh.Group14()
	encode := func(v *big.Int) []byte { return v.FillBytes(make([]byte, dh.PublicValueSize)) }
	for name, value := range map[string][]byte{
		"0":   encode(big.NewInt(0)),
		"1":   encode(big.NewInt(1)),
		"p-1": encode(new(big.Int).Sub(group.P(), big.NewInt(1))),
	} {
		created, err := alice.chats.CreateChat(actx, &apiv1.CreateChatRequest{PeerLogin: "bob", Params: &apiv1.ChatParams{
			Algorithm: apiv1.Algorithm_ALGORITHM_RC6, KeyBits: 256,
			CipherMode: apiv1.CipherMode_CIPHER_MODE_CBC, PaddingMode: apiv1.PaddingMode_PADDING_MODE_PKCS7,
		}})
		if err != nil {
			t.Fatal(err)
		}
		id := created.GetChat().GetChatId()
		if _, err := alice.keys.PublishPublicValue(actx, &apiv1.PublishPublicValueRequest{ChatId: id, PublicValue: value}); err != nil {
			t.Fatalf("%s: сервер проверяет только длину: %v", name, err)
		}
		got, err := bob.keys.GetPeerPublicValue(bctx, &apiv1.ChatRef{ChatId: id})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := group.ParsePublic(got.GetPublicValue()); !errors.Is(err, dh.ErrInvalidPublicValue) {
			t.Errorf("%s: клиент принял значение: %v", name, err)
		}
	}
}
