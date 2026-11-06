//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	apiv1 "cryptochat/server/api/v1"
	"cryptochat/server/internal/bootstrap"
	"cryptochat/server/internal/rpc"
)

// world — развёртывание и два пользователя, вошедшие через тестовый клиент.
type world struct {
	t                  *testing.T
	s                  *stack
	chats              apiv1.ChatsClient
	delivery           apiv1.DeliveryClient
	chatcli            string
	aliceFile, bobFile string
	aliceTok, bobTok   string
	ca                 string
}

func (w *world) cli(session, stdin string, args ...string) (string, error) {
	full := append([]string{"-addr", w.s.addr, "-ca", w.ca, "-session", session}, args...)
	cmd := exec.Command(w.chatcli, full...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (w *world) must(session, stdin, contains string, args ...string) string {
	w.t.Helper()
	out, err := w.cli(session, stdin, args...)
	if err != nil || !strings.Contains(out, contains) {
		w.t.Fatalf("chatcli %v: %v\n%s", args[0], err, out)
	}
	return out
}

func (w *world) fails(session, stdin, contains string, args ...string) {
	w.t.Helper()
	out, err := w.cli(session, stdin, args...)
	if err == nil || !strings.Contains(out, contains) {
		w.t.Fatalf("chatcli %v: ожидалось %q: %v\n%s", args[0], contains, err, out)
	}
}

// readyChat создаёт чат, готовый к обмену, и возвращает идентификатор.
func (w *world) readyChat() string {
	w.t.Helper()
	id := w.must(w.aliceFile, "", "", "chat-create", "bob", "RC6", "256", "CBC", "PKCS7")
	w.must(w.bobFile, "", "я_подключён=true", "chat-connect", id)
	w.must(w.aliceFile, hex.EncodeToString(randomValue(w.t))+"\n", "опубликовано", "key-publish", id)
	w.must(w.bobFile, hex.EncodeToString(randomValue(w.t))+"\n", "опубликовано", "key-publish", id)
	return id
}

// queues возвращает очереди чата: имя и остальные столбцы.
func (w *world) queues(id string, columns ...string) map[string]string {
	w.t.Helper()
	args := append([]string{"exec", "-T", "rabbitmq", "rabbitmqctl", "-q", "list_queues", "--no-table-headers", "name"}, columns...)
	out := w.s.must(args...)
	found := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		name, rest, _ := strings.Cut(strings.TrimSpace(line), "\t")
		if strings.Contains(name, id) {
			found[name] = rest
		}
	}
	return found
}

// queuesGone ждёт исчезновения очередей чата.
func (w *world) queuesGone(what, id string, from time.Time, limit time.Duration) {
	w.t.Helper()
	for {
		out, err := w.s.run("exec", "-T", "rabbitmq", "rabbitmqctl", "-q", "list_queues", "--no-table-headers", "name")
		if err == nil && !strings.Contains(out, id) {
			break
		}
		if time.Since(from) > limit+30*time.Second {
			w.t.Fatalf("SC-005: %s: очереди не исчезли: %s", what, out)
		}
		time.Sleep(50 * time.Millisecond)
	}
	took := time.Since(from)
	w.t.Logf("SC-005: %s: очереди исчезли за %v", what, took)
	if took > limit {
		w.t.Errorf("SC-005: %s: %v больше %v", what, took, limit)
	}
}

func randomFragment(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func (w *world) healthIs(want healthpb.HealthCheckResponse_ServingStatus) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := w.s.health.Check(ctx, &healthpb.HealthCheckRequest{})
	return err == nil && r.GetStatus() == want
}

func (w *world) waitHealth(what string, want healthpb.HealthCheckResponse_ServingStatus, limit time.Duration) {
	w.t.Helper()
	start := time.Now()
	for !w.healthIs(want) {
		if time.Since(start) > limit {
			w.t.Fatalf("%s: Health не стал %v за %v", what, want, limit)
		}
		time.Sleep(50 * time.Millisecond)
	}
	w.t.Logf("%s: Health %v за %v", what, want, time.Since(start))
}

func (w *world) restartCount(service string) string {
	w.t.Helper()
	id := strings.TrimSpace(w.s.must("ps", "-q", service))
	out, err := exec.Command("docker", "inspect", "--format", "{{.RestartCount}}", id).CombinedOutput()
	if err != nil {
		w.t.Fatalf("docker inspect: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// openStream открывает поток напрямую через API и ждёт ответа opened.
func (w *world) openStream(token string, id []byte) (apiv1.Delivery_ReceiveFragmentsClient, context.CancelFunc) {
	w.t.Helper()
	ctx, cancel := context.WithCancel(bearer(context.Background(), token))
	w.t.Cleanup(cancel)
	stream, err := w.delivery.ReceiveFragments(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := stream.Send(&apiv1.ReceiveRequest{Kind: &apiv1.ReceiveRequest_Open_{Open: &apiv1.ReceiveRequest_Open{ChatId: id}}}); err != nil {
		w.t.Fatal(err)
	}
	ev, err := stream.Recv()
	if err != nil || ev.GetOpened() == nil {
		w.t.Fatalf("открытие потока: %v, %v", ev, err)
	}
	return stream, cancel
}

func ack(stream apiv1.Delivery_ReceiveFragmentsClient, n uint64) error {
	return stream.Send(&apiv1.ReceiveRequest{Kind: &apiv1.ReceiveRequest_Ack_{Ack: &apiv1.ReceiveRequest_Ack{DeliveryId: n}}})
}

func (w *world) peerConnected(token string, id []byte) bool {
	w.t.Helper()
	ctx, cancel := callCtx()
	defer cancel()
	r, err := w.chats.GetChat(bearer(ctx, token), &apiv1.ChatRef{ChatId: id})
	if err != nil {
		w.t.Fatal(err)
	}
	return r.GetChat().GetPeer().GetConnected()
}

func TestDeliveryEndToEnd(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	s := &stack{t: t, certs: t.TempDir(), compose: filepath.Join(root, "deploy", "compose.yaml"), addr: "localhost:" + port}
	_, _ = s.run("down", "-v", "--remove-orphans")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := s.run("logs", "--no-color", "server")
			t.Logf("журнал сервера:\n%s", out)
		}
		if s.conn != nil {
			_ = s.conn.Close()
		}
		_, _ = s.run("down", "-v", "--remove-orphans")
	})
	s.must("pull", "postgres", "rabbitmq")
	s.must("up", "--build", "-d")
	s.connect()
	s.waitHealthy(2 * time.Minute)
	w := &world{t: t, s: s, chats: apiv1.NewChatsClient(s.conn), delivery: apiv1.NewDeliveryClient(s.conn),
		chatcli: buildChatcli(t, root), ca: filepath.Join(s.certs, bootstrap.ServerCertFile),
		aliceFile: filepath.Join(t.TempDir(), "alice"), bobFile: filepath.Join(t.TempDir(), "bob")}
	const pw = "delivery-e2e-passphrase"
	w.must(w.aliceFile, pw+"\n", "зарегистрирован", "register", "alice")
	w.must(w.aliceFile, pw+"\n", "вход выполнен", "login", "alice")
	w.must(w.bobFile, pw+"\n", "зарегистрирован", "register", "bob")
	w.must(w.bobFile, pw+"\n", "вход выполнен", "login", "bob")
	w.must(w.aliceFile, "", "запрос отправлен", "contact-request", "bob")
	w.must(w.bobFile, "", "готово", "contact-accept", "alice")
	w.aliceTok, w.bobTok = strings.TrimSpace(string(mustRead(t, w.aliceFile))), strings.TrimSpace(string(mustRead(t, w.bobFile)))

	// SC-013: брокер не публикует портов, а его секреты не выходят наружу.
	// У неопубликованного порта Compose печатает пустую строку или адрес с портом 0, в зависимости от версии.
	if out, err := s.run("port", "rabbitmq", "5672"); err == nil && strings.TrimSpace(out) != "" && !strings.HasSuffix(strings.TrimSpace(out), ":0") {
		t.Errorf("SC-013: порт брокера опубликован: %s", out)
	}
	if out := s.must("ps", "--format", "{{.Service}} {{.Ports}}", "rabbitmq"); strings.Contains(out, "->") {
		t.Errorf("SC-013: порты брокера: %s", out)
	}
	entries, err := os.ReadDir(s.certs)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != bootstrap.ServerCertFile {
		t.Errorf("SC-013: папка сертификатов содержит лишнее: %v", entries)
	}

	// SC-001: отправка и получение через тестовый клиент.
	id := w.readyChat()
	rawID, err := hex.DecodeString(id)
	if err != nil {
		t.Fatal(err)
	}
	label := "metka" + hex.EncodeToString(randomFragment(t, 12))
	first := hex.EncodeToString(append([]byte(label), randomFragment(t, 1024)...))
	second := hex.EncodeToString(randomFragment(t, 2048))
	w.must(w.aliceFile, first+"\n\n"+second+"\r\n", "отправлено: 2", "send", id)
	if got := w.queues(id, "messages"); len(got) != 1 || !slices.Contains(mapValues(got), "2") {
		t.Fatalf("SC-001: очереди после отправки: %v", got)
	}
	out := w.must(w.bobFile, "", "", "recv", id, "2")
	if lines := strings.Fields(out); len(lines) != 2 || lines[0] != first || lines[1] != second {
		t.Fatalf("SC-001: получено не то: %d символов", len(out))
	}
	if got := w.queues(id, "messages"); !slices.Contains(mapValues(got), "0") {
		t.Fatalf("SC-001: очередь после подтверждения: %v", got)
	}
	// recv снимает подключение при завершении.
	if w.peerConnected(w.aliceTok, rawID) {
		t.Error("recv не снял подключение")
	}
	w.must(w.bobFile, "", "я_подключён=true", "chat-connect", id)
	// Причины и коды завершения.
	w.fails(w.aliceFile, "не-hex\n", "шестнадцатеричным", "send", id)
	w.fails(w.bobFile, "", "получено фрагментов: 0 из 1", "recv", id, "1", "1")
	w.must(w.bobFile, "", "я_подключён=true", "chat-connect", id)

	// Аргументы очереди заданы контрактом.
	args := w.queues(id, "durable", "auto_delete", "arguments")
	for name, line := range args {
		for _, want := range []string{"true\tfalse", "x-max-length-bytes", "67108864", "x-max-length", "4096", "x-overflow", "reject-publish"} {
			if !strings.Contains(line, want) {
				t.Errorf("аргументы очереди %s: нет %q в %q", name, want, line)
			}
		}
	}

	// SC-009: наибольший фрагмент формата при открытом потоке.
	stream, cancelStream := w.openStream(w.bobTok, rawID)
	var sendMax, deliverMax time.Duration
	for i := 0; i < 20; i++ {
		fragment := randomFragment(t, 53312)
		ctx, cancel := callCtx()
		start := time.Now()
		_, err := w.delivery.SendFragment(bearer(ctx, w.aliceTok), &apiv1.SendFragmentRequest{ChatId: rawID, Fragment: fragment})
		sendTime := time.Since(start)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		ev, err := stream.Recv()
		deliverTime := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		if string(ev.GetFragment().GetFragment()) != string(fragment) {
			t.Fatal("SC-009: фрагмент изменился")
		}
		if err := ack(stream, ev.GetFragment().GetDeliveryId()); err != nil {
			t.Fatal(err)
		}
		sendMax, deliverMax = max(sendMax, sendTime), max(deliverMax, deliverTime)
	}
	t.Logf("SC-009: отправка не дольше %v, доставка не дольше %v", sendMax, deliverMax)
	if sendMax > 200*time.Millisecond {
		t.Errorf("SC-009: отправка %v", sendMax)
	}
	if deliverMax > time.Second {
		t.Errorf("SC-009: доставка %v", deliverMax)
	}

	// Подтверждения дошли; иначе обрыв вернул бы последний фрагмент, как и положено при неподтверждении.
	waitUntil(t, "приём подтверждений", 5*time.Second, func() bool { return slices.Contains(mapValues(w.queues(id, "messages")), "0") })
	// Обрыв клиента снимает подключение, собеседник это видит.
	cancelStream()
	waitUntil(t, "снятие подключения после обрыва", 3*time.Second, func() bool { return !w.peerConnected(w.aliceTok, rawID) })
	w.must(w.bobFile, "", "я_подключён=true", "chat-connect", id)

	// SC-011: ожидающие фрагменты после перезапуска брокера, пересоздания его контейнера и перезапуска сервера.
	pending := hex.EncodeToString(randomFragment(t, 512))
	w.must(w.aliceFile, pending+"\n", "отправлено: 1", "send", id)
	s.must("restart", "rabbitmq")
	w.waitHealth("после restart rabbitmq", healthpb.HealthCheckResponse_SERVING, 60*time.Second)
	pending2 := hex.EncodeToString(randomFragment(t, 512))
	w.must(w.aliceFile, pending2+"\n", "отправлено: 1", "send", id)
	s.must("up", "-d", "--no-deps", "--force-recreate", "rabbitmq")
	s.must("restart", "server")
	s.waitHealthy(2 * time.Minute)
	if !w.peerConnected(w.aliceTok, rawID) {
		t.Error("SC-011: подключение потеряно при перезапуске сервера")
	}
	out = w.must(w.bobFile, "", "", "recv", id, "2", "30")
	if lines := strings.Fields(out); len(lines) != 2 || !slices.Contains(lines, pending) || !slices.Contains(lines, pending2) {
		t.Fatalf("SC-011: после перезапусков: %q", out)
	}
	t.Log("SC-011: ожидающие фрагменты пережили перезапуск брокера, пересоздание контейнера и перезапуск сервера")
	w.must(w.bobFile, "", "я_подключён=true", "chat-connect", id)

	// Остановка сервера с открытым потоком укладывается в предел и завершается штатно.
	stream, cancelStream = w.openStream(w.bobTok, rawID)
	stopStart := time.Now()
	s.must("stop", "server")
	stopTime := time.Since(stopStart)
	t.Logf("остановка сервера с открытым потоком %v", stopTime)
	if stopTime > 10*time.Second {
		t.Errorf("остановка заняла %v", stopTime)
	}
	if _, err := stream.Recv(); err == nil || rpc.ReasonOf(err) != rpc.ReasonServerStopping {
		t.Errorf("статус потока при остановке: %v", err)
	}
	cancelStream()
	if state := s.must("ps", "-a", "--format", "{{.Service}} {{.ExitCode}}", "server"); !strings.Contains(state, "server 0") {
		t.Errorf("код завершения сервера:\n%s", state)
	}
	s.must("start", "server")
	s.waitHealthy(2 * time.Minute)
	if !w.peerConnected(w.aliceTok, rawID) {
		t.Error("остановка сервера сняла подключение")
	}

	// SC-010: сбой брокера при работающем сервере.
	beforeRestarts := w.restartCount("server")
	stream, cancelStream = w.openStream(w.bobTok, rawID)
	s.must("stop", "rabbitmq")
	if _, err := stream.Recv(); rpc.ReasonOf(err) != rpc.ReasonBrokerUnavailable {
		t.Errorf("SC-010: поток при сбое: %v", err)
	}
	cancelStream()
	ctx, cancel := callCtx()
	failStart := time.Now()
	_, err = w.delivery.SendFragment(bearer(ctx, w.aliceTok), &apiv1.SendFragmentRequest{ChatId: rawID, Fragment: []byte("x")})
	failTime := time.Since(failStart)
	cancel()
	if rpc.ReasonOf(err) != rpc.ReasonBrokerUnavailable || status.Code(err) != codes.Unavailable {
		t.Errorf("SC-010: отправка: %v", err)
	}
	t.Logf("SC-010: ошибка за %v", failTime)
	if failTime > 5*time.Second {
		t.Errorf("SC-010: ответ занял %v", failTime)
	}
	w.waitHealth("SC-010: брокер остановлен", healthpb.HealthCheckResponse_NOT_SERVING, 5*time.Second)
	if !w.peerConnected(w.aliceTok, rawID) {
		t.Error("SC-010: сбой брокера снял подключение")
	}
	w.must(w.aliceFile, pw+"\n", "вход выполнен", "login", "alice")
	w.aliceTok = strings.TrimSpace(string(mustRead(t, w.aliceFile)))
	w.must(w.aliceFile, "", "собеседник=bob", "chats")
	w.must(w.aliceFile, "", "контакты: bob", "contacts")
	w.fails(w.aliceFile, "aabb\n", rpc.ReasonBrokerUnavailable, "send", id)
	s.must("start", "rabbitmq")
	recoverStart := time.Now()
	w.waitHealth("SC-010: брокер запущен", healthpb.HealthCheckResponse_SERVING, 30*time.Second)
	w.waitForDelivery(id)
	recovery := time.Since(recoverStart)
	t.Logf("SC-010: восстановление за %v", recovery)
	if recovery > 30*time.Second {
		t.Errorf("SC-010: восстановление заняло %v", recovery)
	}
	if after := w.restartCount("server"); after != beforeRestarts {
		t.Errorf("SC-010: контейнер сервера перезапускался: %s, %s", beforeRestarts, after)
	}
	w.must(w.bobFile, "", "", "recv", id, "1", "30")
	w.must(w.bobFile, "", "я_подключён=true", "chat-connect", id)

	// Запуск сервера при остановленном брокере: сервер не завершается и становится готов, когда брокер появится.
	s.must("stop", "server", "rabbitmq")
	s.must("up", "-d", "--no-deps", "server")
	waitUntil(t, "вход при остановленном брокере", 60*time.Second, func() bool {
		_, err := w.cli(w.aliceFile, pw+"\n", "login", "alice")
		return err == nil
	})
	w.aliceTok = strings.TrimSpace(string(mustRead(t, w.aliceFile)))
	if !w.healthIs(healthpb.HealthCheckResponse_NOT_SERVING) {
		t.Error("сервер готов без брокера")
	}
	s.must("start", "rabbitmq")
	w.waitHealth("запуск без брокера: брокер появился", healthpb.HealthCheckResponse_SERVING, 30*time.Second)

	// SC-005: удаление очередей при закрытии и при удалении контакта.
	w.waitForDelivery(id)
	w.must(w.bobFile, "", "", "recv", id, "1", "30")
	w.must(w.bobFile, "", "я_подключён=true", "chat-connect", id)
	w.must(w.bobFile, "", "закрыт", "chat-close", id)
	w.queuesGone("закрытие чата", id, time.Now(), 5*time.Second)

	byContact := w.readyChat()
	w.must(w.aliceFile, hex.EncodeToString(randomFragment(t, 64))+"\n", "отправлено: 1", "send", byContact)
	viaBroker := w.readyChat()
	w.must(w.aliceFile, hex.EncodeToString(randomFragment(t, 64))+"\n", "отправлено: 1", "send", viaBroker)

	// Закрытие при остановленном брокере: очереди исчезают после его возврата.
	s.must("stop", "rabbitmq")
	w.must(w.aliceFile, "", "закрыт", "chat-close", viaBroker)
	s.must("start", "rabbitmq")
	w.queuesGone("закрытие при остановленном брокере", viaBroker, time.Now(), 30*time.Second)

	w.waitHealth("перед удалением контакта", healthpb.HealthCheckResponse_SERVING, 30*time.Second)
	w.must(w.aliceFile, "", "готово", "contact-remove", "bob")
	w.queuesGone("удаление контакта", byContact, time.Now(), 5*time.Second)

	// SC-013: журналы сервера и брокера не содержат метки, пароля брокера и адреса с ним.
	password := strings.TrimSpace(s.must("exec", "-T", "rabbitmq", "cat", "/secrets/rabbitmq_password"))
	if password == "" {
		t.Fatal("пароль брокера пуст")
	}
	logs := s.must("logs", "--no-color", "server", "rabbitmq", "init")
	if !strings.Contains(logs, "ReceiveFragments") {
		t.Fatal("в журналах нет потока: проверка ничего не значит")
	}
	for name, secret := range map[string]string{
		"метка":         label,
		"метка в hex":   hex.EncodeToString([]byte(label)),
		"фрагмент":      first,
		"пароль":        password,
		"адрес брокера": "amqp://cryptochat",
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("SC-013: журналы содержат: %s", name)
		}
	}
	serverLogs := s.must("logs", "--no-color", "server")
	if strings.Contains(serverLogs, id) {
		t.Error("журнал сервера содержит идентификатор чата")
	}
	dump := s.must("exec", "-T", "postgres", "pg_dump", "-U", "cryptochat", "cryptochat")
	if strings.Contains(dump, hex.EncodeToString([]byte(label))) || strings.Contains(dump, label) {
		t.Error("база содержит метку фрагмента")
	}
}

// waitForDelivery ждёт, пока отправка снова заработает: сервер переподключается к брокеру сам.
func (w *world) waitForDelivery(id string) {
	w.t.Helper()
	waitUntil(w.t, "работающая отправка", 60*time.Second, func() bool {
		_, err := w.cli(w.aliceFile, hex.EncodeToString(randomFragment(w.t, 16))+"\n", "send", id)
		return err == nil
	})
}

func waitUntil(t *testing.T, what string, limit time.Duration, cond func() bool) {
	t.Helper()
	start := time.Now()
	for !cond() {
		if time.Since(start) > limit {
			t.Fatalf("не дождались за %v: %s", limit, what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
