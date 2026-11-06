//go:build e2e

// Сквозная проверка на настоящем Compose: go test -tags e2e ./test/e2e -v -timeout 15m
package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	apiv1 "cryptochat/server/api/v1"
	"cryptochat/server/internal/bootstrap"
	"cryptochat/server/internal/rpc"
)

const (
	project = "cryptochat-e2e"
	port    = "18443"
)

type stack struct {
	t        *testing.T
	certs    string
	compose  string
	addr     string
	conn     *grpc.ClientConn
	accounts apiv1.AccountsClient
	contacts apiv1.ContactsClient
	health   healthpb.HealthClient
}

func (s *stack) run(args ...string) (string, error) {
	base := []string{"compose", "-p", project, "-f", s.compose}
	cmd := exec.Command("docker", append(base, args...)...)
	cmd.Env = append(os.Environ(), "CHAT_PUBLIC_PORT="+port, "CHAT_CERTS_DIR="+s.certs)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (s *stack) must(args ...string) string {
	s.t.Helper()
	out, err := s.run(args...)
	if err != nil {
		s.t.Fatalf("docker compose %v: %v\n%s", args, err, out)
	}
	return out
}

func (s *stack) waitHealthy(limit time.Duration) time.Duration {
	s.t.Helper()
	start := time.Now()
	for time.Since(start) < limit {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		r, err := s.health.Check(ctx, &healthpb.HealthCheckRequest{})
		cancel()
		if err == nil && r.GetStatus() == healthpb.HealthCheckResponse_SERVING {
			return time.Since(start)
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.t.Fatalf("сервер не стал готов за %v\n%s", limit, s.must("logs", "--no-color"))
	return 0
}

func (s *stack) connect() {
	s.t.Helper()
	cfg, err := bootstrap.ClientTLS(filepath.Join(s.certs, bootstrap.ServerCertFile), "localhost")
	if err != nil {
		s.t.Fatal(err)
	}
	conn, err := grpc.NewClient(s.addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		s.t.Fatal(err)
	}
	s.conn = conn
	s.accounts = apiv1.NewAccountsClient(conn)
	s.contacts = apiv1.NewContactsClient(conn)
	s.health = healthpb.NewHealthClient(conn)
}

func callCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func bearer(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

func TestEndToEnd(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	s := &stack{t: t, certs: t.TempDir(), compose: filepath.Join(root, "deploy", "compose.yaml"), addr: "localhost:" + port}
	_, _ = s.run("down", "-v", "--remove-orphans")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := s.run("logs", "--no-color")
			t.Logf("журналы контейнеров:\n%s", out)
		}
		if s.conn != nil {
			_ = s.conn.Close()
		}
		_, _ = s.run("down", "-v", "--remove-orphans")
	})

	// Образы собираются отдельно: SC-005 относится ко времени после загрузки образов.
	s.must("build")
	s.must("pull", "postgres")

	start := time.Now()
	s.must("up", "-d")
	s.connect()
	s.waitHealthy(2 * time.Minute)
	startup := time.Since(start)
	t.Logf("SC-005: запуск до готовности %v", startup)
	if startup > 2*time.Minute {
		t.Errorf("SC-005: запуск занял %v", startup)
	}

	const alicePass, bobPass = "alice-very-secret-passphrase", "bob-other-secret-passphrase"
	chatcli := buildChatcli(t, root)
	cli := func(session, stdin string, args ...string) (string, error) {
		full := append([]string{"-addr", s.addr, "-ca", filepath.Join(s.certs, bootstrap.ServerCertFile), "-session", session}, args...)
		cmd := exec.Command(chatcli, full...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	aliceFile, bobFile := filepath.Join(t.TempDir(), "alice"), filepath.Join(t.TempDir(), "bob")
	steps := []struct {
		session, stdin string
		args           []string
		contains       string
	}{
		{aliceFile, alicePass + "\r\n", []string{"register", "alice"}, "зарегистрирован"},
		{aliceFile, alicePass + "\n", []string{"login", "alice"}, "вход выполнен"},
		{bobFile, bobPass + "\n", []string{"register", "Bob"}, "зарегистрирован"},
		{bobFile, bobPass, []string{"login", "bob"}, "вход выполнен"},
		{aliceFile, "", []string{"contact-request", "bob"}, "запрос отправлен"},
		{bobFile, "", []string{"contacts"}, "входящие: alice"},
		{bobFile, "", []string{"contact-accept", "alice"}, "готово"},
		{aliceFile, "", []string{"contacts"}, "контакты: Bob"},
	}
	for _, step := range steps {
		out, err := cli(step.session, step.stdin, step.args...)
		if err != nil || !strings.Contains(out, step.contains) {
			t.Fatalf("chatcli %v: %v\n%s", step.args, err, out)
		}
	}
	out, err := cli(aliceFile, "", "contact-request", "bob")
	if err == nil || !strings.Contains(out, rpc.ReasonAlreadyContact) {
		t.Errorf("ошибка клиента: %v\n%s", err, out)
	}
	aliceToken, err := os.ReadFile(aliceFile)
	if err != nil {
		t.Fatal(err)
	}

	// Запомнить сессию из прямого входа: её тоже нельзя найти ни в базе, ни в журналах.
	ctx, cancel := callCtx()
	loginStart := time.Now()
	direct, err := s.accounts.Login(ctx, &apiv1.LoginRequest{Login: "alice", Password: alicePass})
	loginTime := time.Since(loginStart)
	cancel()
	if err != nil {
		t.Fatal(err)
	}

	// SC-009.
	t.Logf("SC-009: вход %v", loginTime)
	if loginTime > time.Second {
		t.Errorf("SC-009: вход занял %v", loginTime)
	}
	ctx, cancel = callCtx()
	listStart := time.Now()
	if _, err := s.contacts.ListContacts(bearer(ctx, direct.GetToken()), &apiv1.ListContactsRequest{}); err != nil {
		t.Fatal(err)
	}
	listTime := time.Since(listStart)
	cancel()
	t.Logf("SC-009: список контактов %v", listTime)
	if listTime > 200*time.Millisecond {
		t.Errorf("SC-009: операция заняла %v", listTime)
	}

	// SC-002: ни паролей, ни сессий в базе и журналах.
	dump := s.must("exec", "-T", "postgres", "pg_dump", "-U", "cryptochat", "cryptochat")
	logs := s.must("logs", "--no-color")
	if !strings.Contains(dump, "$argon2id$") {
		t.Fatal("в дампе нет хешей: проверка ничего не значит")
	}
	for name, haystack := range map[string]string{"база": dump, "журналы": logs} {
		for _, secret := range []string{alicePass, bobPass, string(aliceToken), direct.GetToken()} {
			if strings.Contains(haystack, secret) {
				t.Errorf("SC-002: %s содержит секрет", name)
			}
		}
	}
	if strings.Contains(logs, "$argon2id$") {
		t.Error("SC-002: журналы содержат хеш пароля")
	}

	// SC-007: без защиты канала соединение не проходит.
	plain, err := grpc.NewClient(s.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	_, err = healthpb.NewHealthClient(plain).Check(ctx, &healthpb.HealthCheckRequest{})
	cancel()
	_ = plain.Close()
	if err == nil {
		t.Error("SC-007: соединение без TLS прошло")
	}
	// TLS 1.2 тоже отвергается.
	raw, err := net.DialTimeout("tcp", s.addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	old := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12}) //nolint:gosec // проверка отказа
	if err := old.Handshake(); err == nil {
		t.Error("SC-007: TLS 1.2 принят")
	}
	_ = raw.Close()

	// SC-008: сбой и восстановление хранилища.
	s.must("stop", "postgres")
	ctx, cancel = callCtx()
	failStart := time.Now()
	_, err = s.contacts.ListContacts(bearer(ctx, direct.GetToken()), &apiv1.ListContactsRequest{})
	failTime := time.Since(failStart)
	cancel()
	if rpc.ReasonOf(err) != rpc.ReasonStoreUnavailable {
		t.Errorf("SC-008: ошибка %v (%s), код %v", err, rpc.ReasonOf(err), status.Code(err))
	}
	t.Logf("SC-008: ошибка за %v", failTime)
	if failTime > 5*time.Second {
		t.Errorf("SC-008: ответ занял %v", failTime)
	}
	s.must("start", "postgres")
	recoverStart := time.Now()
	s.waitForList(direct.GetToken(), 60*time.Second)
	recovery := time.Since(recoverStart)
	t.Logf("SC-008: восстановление за %v", recovery)
	if recovery > 30*time.Second {
		t.Errorf("SC-008: восстановление заняло %v", recovery)
	}

	// Штатное завершение по сигналу.
	s.must("stop", "server")
	state := s.must("ps", "-a", "--format", "{{.Service}} {{.ExitCode}}")
	if !strings.Contains(state, "server 0") {
		t.Errorf("код завершения сервера:\n%s", state)
	}

	// SC-006: данные и вход после перезапуска всего стека, без удаления томов.
	s.must("down")
	s.must("up", "-d")
	s.waitHealthy(2 * time.Minute)
	ctx, cancel = callCtx()
	again, err := s.accounts.Login(ctx, &apiv1.LoginRequest{Login: "BOB", Password: bobPass})
	cancel()
	if err != nil {
		t.Fatalf("SC-006: вход после перезапуска: %v", err)
	}
	ctx, cancel = callCtx()
	lists, err := s.contacts.ListContacts(bearer(ctx, again.GetToken()), &apiv1.ListContactsRequest{})
	cancel()
	if err != nil || len(lists.GetContacts()) != 1 || lists.GetContacts()[0].GetLogin() != "alice" {
		t.Fatalf("SC-006: контакты после перезапуска: %+v, %v", lists, err)
	}

	// Новый стек с нуля создаёт другой сертификат, а клиентский файл обновляется.
	before := mustRead(t, filepath.Join(s.certs, bootstrap.ServerCertFile))
	_ = s.conn.Close()
	s.must("down", "-v")
	s.must("up", "-d")
	s.connect()
	s.waitHealthy(2 * time.Minute)
	if bytes.Equal(before, mustRead(t, filepath.Join(s.certs, bootstrap.ServerCertFile))) {
		t.Error("после down -v сертификат не обновился")
	}
}

func (s *stack) waitForList(token string, limit time.Duration) {
	s.t.Helper()
	deadline := time.Now().Add(limit)
	var last error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := s.contacts.ListContacts(bearer(ctx, token), &apiv1.ListContactsRequest{})
		cancel()
		if err == nil {
			return
		}
		last = err
		time.Sleep(200 * time.Millisecond)
	}
	s.t.Fatalf("сервер не восстановился: %v", last)
}

func buildChatcli(t *testing.T, root string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "chatcli.exe")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/chatcli")
	cmd.Dir = filepath.Join(root, "server")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("сборка chatcli: %v\n%s", err, b)
	}
	return out
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
