//go:build e2e && devcipher

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"cryptochat/core/algorithms"
	"cryptochat/core/cipher"
	"cryptochat/core/dh"
	"cryptochat/core/message"
	"cryptochat/core/modes"
	"cryptochat/core/protocol"
	apiv1 "cryptochat/server/api/v1"
)

const brokerPort = "35673"

// deployment — Compose с дополнительным файлом, публикующим порт брокера.
type deployment struct {
	t                     *testing.T
	file, override, certs string
}

func (d *deployment) run(args ...string) (string, error) {
	base := []string{"compose", "-p", project, "-f", d.file, "-f", d.override}
	cmd := exec.Command("docker", append(base, args...)...)
	cmd.Env = append(os.Environ(), "CHAT_PUBLIC_PORT="+port, "CHAT_CERTS_DIR="+d.certs)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (d *deployment) must(args ...string) string {
	d.t.Helper()
	out, err := d.run(args...)
	if err != nil {
		d.t.Fatalf("docker compose %v: %v\n%s", args, err, out)
	}
	return out
}

// queues возвращает очереди чата и число сообщений в них.
func (d *deployment) queues(chatID []byte) map[string]string {
	d.t.Helper()
	out := d.must("exec", "-T", "rabbitmq", "rabbitmqctl", "-q", "list_queues", "name", "messages", "--no-table-headers")
	found := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.Contains(fields[0], hex.EncodeToString(chatID)) {
			found[fields[0]] = fields[1]
		}
	}
	return found
}

// memWriter — io.WriterAt в памяти.
type memWriter struct {
	mu   sync.Mutex
	data []byte
}

func (m *memWriter) WriteAt(p []byte, off int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if end := off + int64(len(p)); end > int64(len(m.data)) {
		m.data = append(m.data, make([]byte, end-int64(len(m.data)))...)
	}
	copy(m.data[off:], p)
	return len(p), nil
}

// party — сторона чата с сеансовым ключом.
type party struct {
	*client
	delivery apiv1.DeliveryClient
	params   message.Params
	chatID   []byte
}

func (p *party) send(fragment []byte) error {
	ctx, cancel := p.ctx()
	defer cancel()
	_, err := p.delivery.SendFragment(ctx, &apiv1.SendFragmentRequest{ChatId: p.chatID, Fragment: fragment})
	return err
}

// seal шифрует сообщение и возвращает сериализованные фрагменты.
func (p *party) seal(meta message.Meta, data []byte, emit func([]byte) error) error {
	var id [protocol.IDSize]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	return message.Seal(context.Background(), p.params, id, meta, bytes.NewReader(data), func(f *protocol.Fragment) error {
		wire, err := f.Marshal()
		if err != nil {
			return err
		}
		return emit(wire)
	}, nil)
}

// receiver — открытый поток получения.
type receiver struct {
	t      *testing.T
	stream apiv1.Delivery_ReceiveFragmentsClient
	cancel context.CancelFunc
}

func (p *party) open(t *testing.T) *receiver {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+p.token)
	stream, err := p.delivery.ReceiveFragments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&apiv1.ReceiveRequest{Kind: &apiv1.ReceiveRequest_Open_{Open: &apiv1.ReceiveRequest_Open{ChatId: p.chatID}}}); err != nil {
		t.Fatal(err)
	}
	ev, err := stream.Recv()
	if err != nil || ev.GetOpened() == nil {
		t.Fatalf("открытие потока: %v, %v", ev, err)
	}
	return &receiver{t: t, stream: stream, cancel: cancel}
}

func (r *receiver) next() *apiv1.FragmentDelivery {
	r.t.Helper()
	ev, err := r.stream.Recv()
	if err != nil {
		r.t.Fatalf("Recv: %v", err)
	}
	if ev.GetFragment() == nil {
		r.t.Fatalf("событие %v", ev)
	}
	return ev.GetFragment()
}

func (r *receiver) ack(d *apiv1.FragmentDelivery) {
	r.t.Helper()
	err := r.stream.Send(&apiv1.ReceiveRequest{Kind: &apiv1.ReceiveRequest_Ack_{Ack: &apiv1.ReceiveRequest_Ack{DeliveryId: d.GetDeliveryId()}}})
	if err != nil {
		r.t.Fatalf("ack: %v", err)
	}
}

// assembly собирает одно сообщение из выдач и учитывает повторы.
type assembly struct {
	t      *testing.T
	params message.Params
	out    memWriter
	asm    *message.Assembler
	seen   map[uint32]bool
	total  uint32
	dups   int
}

func (p *party) newAssembly(t *testing.T) *assembly {
	return &assembly{t: t, params: p.params, seen: map[uint32]bool{}}
}

func (a *assembly) add(body []byte) {
	a.t.Helper()
	f, err := protocol.Unmarshal(body)
	if err != nil {
		a.t.Fatalf("Unmarshal: %v", err)
	}
	if a.asm == nil {
		if a.asm, err = message.NewAssembler(a.params, f.MessageID, &a.out, nil); err != nil {
			a.t.Fatal(err)
		}
		a.total = f.Total
	}
	if err := a.asm.Add(context.Background(), f); err != nil {
		a.t.Fatalf("Add: %v", err)
	}
	if a.seen[f.Index] {
		a.dups++
	}
	a.seen[f.Index] = true
}

func (a *assembly) complete() bool { return a.asm != nil && uint32(len(a.seen)) == a.total }

func (a *assembly) finish() message.Meta {
	a.t.Helper()
	meta, err := a.asm.Finish()
	if err != nil {
		a.t.Fatalf("Finish: %v", err)
	}
	return meta
}

// hasReason ищет причину в подробностях ответа; разбирать ErrorInfo тесту незачем.
func hasReason(err error, reason string) bool {
	for _, d := range status.Convert(err).Proto().GetDetails() {
		if bytes.Contains(d.GetValue(), []byte(reason)) {
			return true
		}
	}
	return false
}

func randomLabel(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return "metka" + hex.EncodeToString(raw)
}

// Два клиента с криптоядром и тестовым шифром передают текст и файл через настоящий брокер; в брокере, на его
// диске, в журналах и базе только шифротекст: go test -tags e2e,devcipher ./... -v -timeout 20m
func TestDeliveryEndToEnd(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	d := &deployment{t: t, file: filepath.Join(root, "deploy", "compose.yaml"), override: filepath.Join(root, "integration", "testdata", "broker-port.yaml"), certs: t.TempDir()}
	_, _ = d.run("down", "-v", "--remove-orphans")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := d.run("logs", "--no-color", "server")
			t.Logf("журнал сервера:\n%s", out)
		}
		_, _ = d.run("down", "-v", "--remove-orphans")
	})
	d.must("up", "--build", "-d")

	conn := dial(t, filepath.Join(d.certs, "server.crt"))
	waitReady(t, conn)
	alice, bob := newClient(t, conn, "alice"), newClient(t, conn, "bob")
	callAlice, cancelAlice := alice.ctx()
	defer cancelAlice()
	if _, err := alice.con.SendRequest(callAlice, &apiv1.SendContactRequest{Login: "bob"}); err != nil {
		t.Fatal(err)
	}
	callBob, cancelBob := bob.ctx()
	defer cancelBob()
	if _, err := bob.con.AcceptRequest(callBob, &apiv1.ContactLogin{Login: "alice"}); err != nil {
		t.Fatal(err)
	}

	// Чат на сервере с параметром RC6: сервер его только записывает, а шифр выбирают клиенты.
	created, err := alice.chats.CreateChat(callAlice, &apiv1.CreateChatRequest{PeerLogin: "bob", Params: &apiv1.ChatParams{
		Algorithm: apiv1.Algorithm_ALGORITHM_RC6, KeyBits: 256,
		CipherMode: apiv1.CipherMode_CIPHER_MODE_CBC, PaddingMode: apiv1.PaddingMode_PADDING_MODE_PKCS7,
	}})
	if err != nil {
		t.Fatal(err)
	}
	chatID := created.GetChat().GetChatId()
	if _, err := bob.chats.ConnectChat(callBob, &apiv1.ChatRef{ChatId: chatID}); err != nil {
		t.Fatal(err)
	}
	group := dh.Group14()
	pairA, err := group.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	pairB, err := group.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.keys.PublishPublicValue(callAlice, &apiv1.PublishPublicValueRequest{ChatId: chatID, PublicValue: pairA.PublicBytes()}); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.keys.PublishPublicValue(callBob, &apiv1.PublishPublicValueRequest{ChatId: chatID, PublicValue: pairB.PublicBytes()}); err != nil {
		t.Fatal(err)
	}
	keyA := alice.sessionKey(pairA, group, chatID, algorithms.DevAlgorithm, 256)
	keyB := bob.sessionKey(pairB, group, chatID, algorithms.DevAlgorithm, 256)
	if !bytes.Equal(keyA, keyB) {
		t.Fatal("сеансовые ключи различаются")
	}
	settings := cipher.Settings{Algorithm: algorithms.DevAlgorithm, KeyBits: 256, Mode: modes.CBC, Padding: modes.PKCS7}
	var binding [protocol.IDSize]byte
	copy(binding[:], chatID)
	sender := &party{client: alice, delivery: apiv1.NewDeliveryClient(conn), chatID: chatID, params: message.Params{Settings: settings, Key: keyA, ChatID: binding}}
	recipient := &party{client: bob, delivery: apiv1.NewDeliveryClient(conn), chatID: chatID, params: message.Params{Settings: settings, Key: keyB, ChatID: binding}}

	// (1) Текст с уникальными меткой, именем и типом.
	label, nameLabel, typeLabel := randomLabel(t), randomLabel(t), randomLabel(t)
	text := []byte("секретный текст " + label + " конец")
	meta := message.Meta{Name: nameLabel, Type: typeLabel, TotalSize: int64(len(text))}
	var sent [][]byte
	if err := sender.seal(meta, text, func(wire []byte) error {
		sent = append(sent, wire)
		return sender.send(wire)
	}); err != nil {
		t.Fatal(err)
	}
	// Файл из нескольких фрагментов отправляется до открытия потока, чтобы проверить больше сообщений брокера.
	file := make([]byte, 3<<20)
	if _, err := rand.Read(file); err != nil {
		t.Fatal(err)
	}
	for _, at := range []int{0, 70000, 1 << 20, len(file) - len(label)} {
		copy(file[at:], label)
	}
	fileMeta := message.Meta{Name: nameLabel + ".bin", Type: typeLabel, TotalSize: int64(len(file))}
	var fileFragments [][]byte
	if err := sender.seal(fileMeta, file, func(wire []byte) error {
		fileFragments = append(fileFragments, wire)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(fileFragments) < 2 {
		t.Fatalf("файл занял %d фрагмент", len(fileFragments))
	}
	if err := sender.send(fileFragments[0]); err != nil {
		t.Fatal(err)
	}
	sent = append(sent, fileFragments[0])

	// (2) Поток получателя не открыт: содержимое очереди читается напрямую и возвращается в очередь.
	queues := d.queues(chatID)
	if len(queues) != 1 {
		t.Fatalf("очереди чата: %v", queues)
	}
	var queueName string
	for q := range queues {
		queueName = q
	}
	inspectBroker(t, d, queueName, sent, []string{label, nameLabel, typeLabel})

	// (3) Получатель собирает текст; подтверждение идёт после Add. Брокер уже выдавал эти фрагменты.
	stream := recipient.open(t)
	asm := recipient.newAssembly(t)
	var firstOfFile []byte
	for i := 0; i < len(sent); i++ {
		// Порядок при повторной выдаче не гарантируется.
		f := stream.next()
		if !f.GetRedelivered() {
			t.Error("фрагмент, прочитанный из очереди, не отмечен как повторный")
		}
		if bytes.Equal(f.GetFragment(), fileFragments[0]) {
			firstOfFile = f.GetFragment()
		} else {
			asm.add(f.GetFragment())
		}
		stream.ack(f)
	}
	if !asm.complete() {
		t.Fatalf("текст не собран: %d из %d", len(asm.seen), asm.total)
	}
	gotMeta := asm.finish()
	if gotMeta != meta || !bytes.Equal(asm.out.data, text) {
		t.Fatalf("текст: %+v, %q", gotMeta, asm.out.data)
	}
	t.Log("SC-002: текст, имя и тип совпали")

	// (4) Файл около 3 МиБ при открытом потоке.
	sendDone := make(chan error, 1)
	go func() {
		for _, wire := range fileFragments[1:] {
			if err := sender.send(wire); err != nil {
				sendDone <- err
				return
			}
		}
		sendDone <- nil
	}()
	asm = recipient.newAssembly(t)
	asm.add(firstOfFile)
	for !asm.complete() {
		f := stream.next()
		asm.add(f.GetFragment())
		stream.ack(f)
	}
	if err := <-sendDone; err != nil {
		t.Fatal(err)
	}
	if gotMeta := asm.finish(); gotMeta != fileMeta || !bytes.Equal(asm.out.data, file) {
		t.Fatalf("файл: %+v, длина %d", gotMeta, len(asm.out.data))
	}
	t.Logf("SC-002: файл %d байт и %d фрагментов совпал байт в байт", len(file), len(fileFragments))

	// (5) Наибольший кусок с наибольшими именем и типом; граница предела сервера.
	largest := make([]byte, message.ChunkSize)
	if _, err := rand.Read(largest); err != nil {
		t.Fatal(err)
	}
	largestMeta := message.Meta{Name: strings.Repeat("n", message.MaxNameLen), Type: strings.Repeat("t", message.MaxTypeLen), TotalSize: int64(len(largest))}
	var largestWire []byte
	if err := sender.seal(largestMeta, largest, func(wire []byte) error {
		largestWire = wire
		return sender.send(wire)
	}); err != nil {
		t.Fatalf("SC-008: наибольший фрагмент: %v", err)
	}
	if len(largestWire) > protocol.MaxWireSize {
		t.Fatalf("SC-008: фрагмент %d больше формата", len(largestWire))
	}
	t.Logf("SC-008: наибольший фрагмент %d байт, запас до предела сервера %d байт", len(largestWire), 61440-len(largestWire))
	asm = recipient.newAssembly(t)
	f := stream.next()
	asm.add(f.GetFragment())
	stream.ack(f)
	if gotMeta := asm.finish(); gotMeta != largestMeta || !bytes.Equal(asm.out.data, largest) {
		t.Fatal("SC-008: наибольший фрагмент не совпал")
	}
	opaque := make([]byte, 61440)
	if _, err := rand.Read(opaque); err != nil {
		t.Fatal(err)
	}
	if err := sender.send(opaque); err != nil {
		t.Fatalf("SC-008: непрозрачные 61440 байт: %v", err)
	}
	f = stream.next()
	if !bytes.Equal(f.GetFragment(), opaque) {
		t.Fatal("SC-008: непрозрачные байты изменились")
	}
	stream.ack(f)
	if err := sender.send(make([]byte, 61441)); !hasReason(err, "FRAGMENT_TOO_LARGE") {
		t.Fatalf("SC-008: 61441 байт: %v", err)
	}

	// (6) Обрыв потока до подтверждения половины фрагментов: повторы не портят результат.
	second := make([]byte, 40*message.ChunkSize+17)
	if _, err := rand.Read(second); err != nil {
		t.Fatal(err)
	}
	secondMeta := message.Meta{Name: "вторая часть", TotalSize: int64(len(second))}
	if err := sender.seal(secondMeta, second, sender.send); err != nil {
		t.Fatal(err)
	}
	asm = recipient.newAssembly(t)
	for i := 0; i < 20; i++ {
		f := stream.next()
		asm.add(f.GetFragment())
		if i < 10 {
			stream.ack(f)
		}
	}
	stream.cancel()
	stream = recipient.open(t)
	redelivered := 0
	for !asm.complete() {
		f := stream.next()
		if f.GetRedelivered() {
			redelivered++
		}
		asm.add(f.GetFragment())
		stream.ack(f)
	}
	// Подтверждается всё, что брокер ещё держит: повторы могли прийти после последнего нового фрагмента.
	rest := make(chan *apiv1.FragmentDelivery)
	go func(s *receiver) {
		for {
			ev, err := s.stream.Recv()
			if err != nil {
				return
			}
			select {
			case rest <- ev.GetFragment():
			case <-s.stream.Context().Done():
				return
			}
		}
	}(stream)
	drainDeadline := time.Now().Add(time.Minute)
	for d.queues(chatID)[queueName] != "0" {
		if time.Now().After(drainDeadline) {
			t.Fatalf("очередь не опустела: %v", d.queues(chatID))
		}
		select {
		case f := <-rest:
			if f.GetRedelivered() {
				redelivered++
			}
			asm.add(f.GetFragment())
			stream.ack(f)
		case <-time.After(200 * time.Millisecond):
		}
	}
	if gotMeta := asm.finish(); gotMeta != secondMeta || !bytes.Equal(asm.out.data, second) {
		t.Fatal("SC-004: результат после обрыва не совпал")
	}
	t.Logf("SC-004: повторных выдач %d, повторов при сборке %d", redelivered, asm.dups)
	if asm.dups == 0 || redelivered == 0 {
		t.Error("SC-004: повторов не было, проверка ничего не доказала")
	}

	// (7) После подтверждения сообщений нет, метки нет нигде.
	for q, n := range d.queues(chatID) {
		if n != "0" {
			t.Errorf("SC-004: очередь %s содержит %s", q, n)
		}
	}
	searchLabels(t, d, []string{label, nameLabel, typeLabel})

	// (8) Закрытие чата удаляет очереди.
	closeStart := time.Now()
	if _, err := alice.chats.CloseChat(callAlice, &apiv1.ChatRef{ChatId: chatID}); err != nil {
		t.Fatal(err)
	}
	for len(d.queues(chatID)) != 0 {
		if time.Since(closeStart) > 5*time.Second {
			t.Fatalf("SC-005: очереди после закрытия: %v", d.queues(chatID))
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("SC-005: очереди исчезли за %v", time.Since(closeStart))
}

// inspectBroker читает очередь без подтверждения и возвращает сообщения обратно: тела байт в байт равны
// отправленным, а других свойств, кроме заданных контрактом, нет.
func inspectBroker(t *testing.T, d *deployment, queue string, sent [][]byte, labels []string) {
	t.Helper()
	password := strings.TrimSpace(d.must("exec", "-T", "rabbitmq", "cat", "/secrets/rabbitmq_password"))
	u := url.URL{Scheme: "amqp", User: url.UserPassword("cryptochat", password), Host: "127.0.0.1:" + brokerPort, Path: "/"}
	conn, err := amqp.Dial(u.String())
	if err != nil {
		t.Fatal(errors.New("подключение к опубликованному порту брокера не удалось"))
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	want := map[string]int{}
	for _, body := range sent {
		want[string(body)]++
	}
	var got []amqp.Delivery
	for {
		m, ok, err := ch.Get(queue, false)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		got = append(got, m)
	}
	if len(got) != len(sent) {
		t.Fatalf("SC-003: сообщений %d, отправлено %d", len(got), len(sent))
	}
	for _, m := range got {
		want[string(m.Body)]--
		if m.ContentType != "application/octet-stream" || m.DeliveryMode != amqp.Persistent {
			t.Errorf("SC-003: свойства: %q, %d", m.ContentType, m.DeliveryMode)
		}
		if len(m.Headers) != 0 || m.ContentEncoding != "" || m.Priority != 0 || m.CorrelationId != "" || m.ReplyTo != "" ||
			m.Expiration != "" || m.MessageId != "" || !m.Timestamp.IsZero() || m.Type != "" || m.UserId != "" || m.AppId != "" {
			t.Errorf("SC-003: лишние свойства: %+v", fmt.Sprintf("%v %q %d %q %q %q %q %v %q %q %q", m.Headers, m.ContentEncoding, m.Priority, m.CorrelationId, m.ReplyTo, m.Expiration, m.MessageId, m.Timestamp, m.Type, m.UserId, m.AppId))
		}
		if m.Exchange != "" || m.RoutingKey != queue {
			t.Errorf("SC-003: маршрут %q, %q", m.Exchange, m.RoutingKey)
		}
		for _, label := range labels {
			if bytes.Contains(m.Body, []byte(label)) || bytes.Contains(m.Body, []byte(hex.EncodeToString([]byte(label)))) {
				t.Errorf("SC-003: метка в теле сообщения брокера")
			}
		}
	}
	for _, n := range want {
		if n != 0 {
			t.Fatal("SC-003: тела сообщений не совпали с отправленными")
		}
	}
	for _, m := range got {
		if err := m.Nack(false, true); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("SC-003: %d сообщений в брокере совпали с отправленными байт в байт", len(got))
}

// searchLabels ищет метки в данных брокера на диске, журналах всех контейнеров и дампе базы.
func searchLabels(t *testing.T, d *deployment, labels []string) {
	t.Helper()
	files := d.must("exec", "-T", "rabbitmq", "find", "/var/lib/rabbitmq", "-type", "f")
	if !strings.Contains(files, "/var/lib/rabbitmq/") {
		t.Fatal("в данных брокера нет файлов: поиск ничего не значит")
	}
	logs := d.must("logs", "--no-color")
	if !strings.Contains(logs, "ReceiveFragments") {
		t.Fatal("в журналах нет потока: поиск ничего не значит")
	}
	dump := d.must("exec", "-T", "postgres", "pg_dump", "-U", "cryptochat", "cryptochat")
	if !strings.Contains(dump, "chat_participants") {
		t.Fatal("в дампе нет чатов: поиск ничего не значит")
	}
	for _, label := range labels {
		for _, needle := range []string{label, hex.EncodeToString([]byte(label))} {
			out, err := d.run("exec", "-T", "rabbitmq", "grep", "-rl", needle, "/var/lib/rabbitmq")
			if err == nil || strings.Contains(out, "/var/lib/rabbitmq/") {
				t.Errorf("SC-003: метка на диске брокера: %s", out)
			}
			if strings.Contains(logs, needle) {
				t.Error("SC-003: метка в журналах")
			}
			if strings.Contains(dump, needle) {
				t.Error("SC-003: метка в базе")
			}
		}
	}
	t.Log("SC-003: метка не найдена на диске брокера, в журналах и базе")
}
