package rpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apiv1 "cryptochat/server/api/v1"
	"cryptochat/server/internal/delivery"
	"cryptochat/server/internal/store"
)

// readyChat создаёт чат с подключёнными участниками и опубликованными значениями.
func readyChat(t *testing.T, h *harness) (alice, bob string, id []byte) {
	t.Helper()
	alice, bob = friends(t, h)
	id = createChat(t, h, alice)
	if _, err := h.chats.ConnectChat(auth(bob), &apiv1.ChatRef{ChatId: id}); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{alice, bob} {
		_, err := h.keys.PublishPublicValue(auth(token), &apiv1.PublishPublicValueRequest{ChatId: id, PublicValue: randomBytes(t, 256)})
		if err != nil {
			t.Fatal(err)
		}
	}
	return alice, bob, id
}

func (h *harness) sendFragment(token string, id, fragment []byte) error {
	_, err := h.delivery.SendFragment(auth(token), &apiv1.SendFragmentRequest{ChatId: id, Fragment: fragment})
	return err
}

type receiveStream = grpc.BidiStreamingClient[apiv1.ReceiveRequest, apiv1.ReceiveEvent]

// clientStream — открытый поток; отмена его контекста имитирует обрыв клиента.
type clientStream struct {
	t      *testing.T
	s      receiveStream
	cancel context.CancelFunc
}

func openMessage(id []byte) *apiv1.ReceiveRequest {
	return &apiv1.ReceiveRequest{Kind: &apiv1.ReceiveRequest_Open_{Open: &apiv1.ReceiveRequest_Open{ChatId: id}}}
}

func ackMessage(n uint64) *apiv1.ReceiveRequest {
	return &apiv1.ReceiveRequest{Kind: &apiv1.ReceiveRequest_Ack_{Ack: &apiv1.ReceiveRequest_Ack{DeliveryId: n}}}
}

func (h *harness) rawStream(t *testing.T, token string) *clientStream {
	t.Helper()
	ctx, cancel := context.WithCancel(auth(token))
	t.Cleanup(cancel)
	s, err := h.delivery.ReceiveFragments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return &clientStream{t: t, s: s, cancel: cancel}
}

// openStream открывает поток и ждёт ответа opened.
func (h *harness) openStream(t *testing.T, token string, id []byte) *clientStream {
	t.Helper()
	c := h.rawStream(t, token)
	if err := c.s.Send(openMessage(id)); err != nil {
		t.Fatal(err)
	}
	ev, err := c.s.Recv()
	if err != nil {
		t.Fatalf("открытие потока: %v", err)
	}
	if ev.GetOpened() == nil {
		t.Fatalf("первое событие: %v", ev)
	}
	return c
}

func (c *clientStream) next() *apiv1.FragmentDelivery {
	c.t.Helper()
	ev, err := c.s.Recv()
	if err != nil {
		c.t.Fatalf("Recv: %v", err)
	}
	if ev.GetFragment() == nil {
		c.t.Fatalf("событие: %v", ev)
	}
	return ev.GetFragment()
}

func (c *clientStream) ack(n uint64) {
	c.t.Helper()
	if err := c.s.Send(ackMessage(n)); err != nil {
		c.t.Fatalf("ack: %v", err)
	}
}

// ends ждёт завершения потока и возвращает статус.
func (c *clientStream) ends(limit time.Duration) (error, time.Duration) {
	c.t.Helper()
	start := time.Now()
	type result struct {
		err error
	}
	done := make(chan result, 1)
	go func() {
		for {
			if _, err := c.s.Recv(); err != nil {
				done <- result{err}
				return
			}
		}
	}()
	select {
	case r := <-done:
		return r.err, time.Since(start)
	case <-time.After(limit):
		c.t.Fatalf("поток не завершился за %v", limit)
	}
	return nil, 0
}

func peerConnected(t *testing.T, h *harness, token string, id []byte) bool {
	t.Helper()
	r, err := h.chats.GetChat(auth(token), &apiv1.ChatRef{ChatId: id})
	if err != nil {
		t.Fatal(err)
	}
	return r.GetChat().GetPeer().GetConnected()
}

func selfConnected(t *testing.T, h *harness, token string, id []byte) bool {
	t.Helper()
	r, err := h.chats.GetChat(auth(token), &apiv1.ChatRef{ChatId: id})
	if err != nil {
		t.Fatal(err)
	}
	return r.GetChat().GetSelf().GetConnected()
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("не дождались: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSendAndReceive(t *testing.T) {
	h := start(t, nil)
	alice, bob, id := readyChat(t, h)

	// Отправка раньше открытия потока.
	early := randomBytes(t, 1000)
	if err := h.sendFragment(alice, id, early); err != nil {
		t.Fatal(err)
	}
	stream := h.openStream(t, bob, id)
	first := stream.next()
	if !bytes.Equal(first.GetFragment(), early) || first.GetRedelivered() {
		t.Fatalf("первый фрагмент: %v", first.GetRedelivered())
	}
	stream.ack(first.GetDeliveryId())

	// Отправка при открытом потоке.
	late := randomBytes(t, 61440)
	if err := h.sendFragment(alice, id, late); err != nil {
		t.Fatal(err)
	}
	second := stream.next()
	if !bytes.Equal(second.GetFragment(), late) {
		t.Fatal("второй фрагмент изменился")
	}
	stream.ack(second.GetDeliveryId())
}

// Встречный обмен: каждый получает только фрагменты собеседника.
func TestBothDirections(t *testing.T) {
	h := start(t, nil)
	alice, bob, id := readyChat(t, h)
	fromAlice, fromBob := []byte("от алисы"), []byte("от боба")
	toAlice := h.openStream(t, alice, id)
	toBob := h.openStream(t, bob, id)
	if err := h.sendFragment(alice, id, fromAlice); err != nil {
		t.Fatal(err)
	}
	if err := h.sendFragment(bob, id, fromBob); err != nil {
		t.Fatal(err)
	}
	if got := toBob.next(); !bytes.Equal(got.GetFragment(), fromAlice) {
		t.Fatalf("бобу: %q", got.GetFragment())
	}
	if got := toAlice.next(); !bytes.Equal(got.GetFragment(), fromBob) {
		t.Fatalf("алисе: %q", got.GetFragment())
	}
}

// Открытие потока до готовности чата подключает участника, и это видно собеседнику.
func TestOpenBeforeReadyConnects(t *testing.T) {
	h := start(t, nil)
	alice, bob := friends(t, h)
	id := createChat(t, h, alice)
	if peerConnected(t, h, alice, id) {
		t.Fatal("собеседник подключён с самого начала")
	}
	h.openStream(t, bob, id)
	if !peerConnected(t, h, alice, id) {
		t.Fatal("открытие потока не подключило участника")
	}
}

func TestSendReasons(t *testing.T) {
	h := start(t, nil)
	alice, bob := friends(t, h)
	id := createChat(t, h, alice)

	expectError(t, h.sendFragment(alice, id, nil), codes.InvalidArgument, ReasonInvalidFragment)
	expectError(t, h.sendFragment(alice, id, make([]byte, delivery.MaxFragmentSize+1)), codes.InvalidArgument, ReasonFragmentTooLarge)
	expectError(t, h.sendFragment(alice, []byte{1, 2}, []byte("x")), codes.InvalidArgument, ReasonInvalidChatID)
	expectError(t, h.sendFragment(alice, id, []byte("x")), codes.FailedPrecondition, ReasonChatNotReady)
	expectError(t, h.sendFragment(bob, id, []byte("x")), codes.FailedPrecondition, ReasonNotConnected)
	if _, err := h.chats.CloseChat(auth(alice), &apiv1.ChatRef{ChatId: id}); err != nil {
		t.Fatal(err)
	}
	expectError(t, h.sendFragment(alice, id, []byte("x")), codes.FailedPrecondition, ReasonChatClosed)

	// Открытие потока: те же причины.
	stream := h.rawStream(t, alice)
	if err := stream.s.Send(openMessage(id)); err != nil {
		t.Fatal(err)
	}
	err, _ := stream.ends(5 * time.Second)
	expectError(t, err, codes.FailedPrecondition, ReasonChatClosed)
	stream = h.rawStream(t, alice)
	if err := stream.s.Send(openMessage([]byte{1})); err != nil {
		t.Fatal(err)
	}
	err, _ = stream.ends(5 * time.Second)
	expectError(t, err, codes.InvalidArgument, ReasonInvalidChatID)
}

// Запрос больше предела отвергает транспорт, сервер продолжает работу.
func TestSendTooLargeForTransport(t *testing.T) {
	h := start(t, nil)
	alice, _, id := readyChat(t, h)
	err := h.sendFragment(alice, id, make([]byte, MaxMessageSize+1))
	if status.Code(err) != codes.ResourceExhausted || ReasonOf(err) != "" {
		t.Fatalf("%v, причина %q", err, ReasonOf(err))
	}
	if err := h.sendFragment(alice, id, []byte("x")); err != nil {
		t.Fatalf("сервер не работает: %v", err)
	}
}

func TestDeliveryRequiresSession(t *testing.T) {
	h := start(t, nil)
	_, err := h.delivery.SendFragment(bg, &apiv1.SendFragmentRequest{ChatId: make([]byte, 16), Fragment: []byte("x")})
	expectError(t, err, codes.Unauthenticated, ReasonUnauthenticated)
	s, err := h.delivery.ReceiveFragments(bg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Recv()
	expectError(t, err, codes.Unauthenticated, ReasonUnauthenticated)
}

// Ответ постороннему совпадает с ответом для случайного идентификатора байт в байт.
func TestStrangerSameAnswer(t *testing.T) {
	h := start(t, nil)
	_, _, id := readyChat(t, h)
	stranger, _ := h.signUp(t, "mallory")
	random := randomBytes(t, 16)

	sendForeign := h.sendFragment(stranger, id, []byte("x"))
	sendRandom := h.sendFragment(stranger, random, []byte("x"))
	expectError(t, sendForeign, codes.NotFound, ReasonChatNotFound)
	if sendForeign.Error() != sendRandom.Error() || !bytes.Equal(statusBytes(t, sendForeign), statusBytes(t, sendRandom)) {
		t.Fatalf("отправка различается:\n%v\n%v", sendForeign, sendRandom)
	}

	open := func(chat []byte) error {
		s := h.rawStream(t, stranger)
		if err := s.s.Send(openMessage(chat)); err != nil {
			t.Fatal(err)
		}
		err, _ := s.ends(5 * time.Second)
		return err
	}
	openForeign, openRandom := open(id), open(random)
	expectError(t, openForeign, codes.NotFound, ReasonChatNotFound)
	if !bytes.Equal(statusBytes(t, openForeign), statusBytes(t, openRandom)) {
		t.Fatalf("поток различается:\n%v\n%v", openForeign, openRandom)
	}
	if len(h.broker.Queues()) != 0 {
		t.Fatalf("посторонний создал очереди: %v", h.broker.Queues())
	}
}

func statusBytes(t *testing.T, err error) []byte {
	t.Helper()
	st, _ := status.FromError(err)
	var out bytes.Buffer
	out.WriteString(st.Code().String())
	out.WriteString(st.Message())
	for _, d := range st.Details() {
		out.WriteString(strings.Join(strings.Fields(toString(d)), " "))
	}
	return out.Bytes()
}

func toString(v any) string {
	if s, ok := v.(interface{ String() string }); ok {
		return s.String()
	}
	return ""
}

// US2: подтверждение, повторная выдача, журнал.

func TestAckAndRedelivery(t *testing.T) {
	h := start(t, nil)
	alice, bob, id := readyChat(t, h)
	var chatID [16]byte
	copy(chatID[:], id)
	bobID := bobUserID(t, h, bob)
	queue := delivery.QueueName(chatID, bobID)
	for _, body := range []string{"первый", "второй"} {
		if err := h.sendFragment(alice, id, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	stream := h.openStream(t, bob, id)
	one, two := stream.next(), stream.next()
	stream.ack(one.GetDeliveryId())
	// Неизвестный и повторный номера игнорируются, поток продолжает работать.
	stream.ack(9999)
	stream.ack(one.GetDeliveryId())
	eventually(t, "подтверждение первого", func() bool {
		n, _ := h.broker.QueueLen(queue)
		return n == 1
	})
	if err := h.sendFragment(alice, id, []byte("третий")); err != nil {
		t.Fatal(err)
	}
	three := stream.next()
	stream.ack(three.GetDeliveryId())
	eventually(t, "подтверждение третьего", func() bool {
		n, _ := h.broker.QueueLen(queue)
		return n == 1
	})

	// Обрыв до подтверждения второго: он приходит снова, а подтверждённые нет.
	stream.cancel()
	eventually(t, "снятие подключения после обрыва", func() bool { return !peerConnected(t, h, alice, id) })
	again := h.openStream(t, bob, id)
	got := again.next()
	if string(got.GetFragment()) != string(two.GetFragment()) || !got.GetRedelivered() {
		t.Fatalf("повтор: %q, %v", got.GetFragment(), got.GetRedelivered())
	}
	again.ack(got.GetDeliveryId())
	eventually(t, "очередь пуста", func() bool {
		n, _ := h.broker.QueueLen(queue)
		return n == 0
	})
}

func bobUserID(t *testing.T, h *harness, token string) string {
	t.Helper()
	u, err := h.deps.Accounts.Authenticate(bg, token)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func sessionHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// flakyDelivery отвечает заданной ошибкой на чтение состояния, пока она задана.
type flakyDelivery struct {
	store.DeliveryStore
	mu  sync.Mutex
	err error
}

func (f *flakyDelivery) set(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *flakyDelivery) DeliveryState(ctx context.Context, id [16]byte, user string) (store.DeliveryState, error) {
	f.mu.Lock()
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return store.DeliveryState{}, err
	}
	return f.DeliveryStore.DeliveryState(ctx, id, user)
}

// В журнале метод, код и длительность, но нет содержимого и идентификатора чата.
func TestDeliveryLogHasNoContent(t *testing.T) {
	h := start(t, nil)
	alice, bob, id := readyChat(t, h)
	label := []byte("секретная-метка-фрагмента")
	if err := h.sendFragment(alice, id, label); err != nil {
		t.Fatal(err)
	}
	stream := h.openStream(t, bob, id)
	d := stream.next()
	stream.ack(d.GetDeliveryId())
	expectError(t, h.sendFragment(alice, []byte{1}, label), codes.InvalidArgument, ReasonInvalidChatID)
	stream.cancel()
	eventually(t, "запись потока в журнал", func() bool {
		return strings.Contains(h.log.String(), apiv1.Delivery_ReceiveFragments_FullMethodName)
	})
	log := h.log.String()
	for _, want := range []string{apiv1.Delivery_SendFragment_FullMethodName, apiv1.Delivery_ReceiveFragments_FullMethodName, "code=Canceled", "duration="} {
		if !strings.Contains(log, want) {
			t.Errorf("в журнале нет %q", want)
		}
	}
	for name, secret := range map[string]string{
		"метка":              string(label),
		"метка в hex":        hex.EncodeToString(label),
		"идентификатор чата": hex.EncodeToString(id),
	} {
		if strings.Contains(log, secret) {
			t.Errorf("журнал содержит: %s", name)
		}
	}
}

// US3: поток соответствует состоянию участника.

func shortOptions() delivery.Options {
	return delivery.Options{AckTimeout: 150 * time.Millisecond, CheckInterval: 10 * time.Millisecond, CleanupInterval: 10 * time.Millisecond}
}

func TestStreamEnds(t *testing.T) {
	// С короткими сроками сценарии идут и под -short; без него те же сценарии проверяются с настоящей
	// сверкой раз в 2 с и границей 3 с.
	opts, limit := shortOptions(), time.Second
	if !testing.Short() {
		opts, limit = delivery.Options{}, 3*time.Second
	}
	opts.AckTimeout = time.Hour

	cases := []struct {
		name   string
		action func(t *testing.T, h *harness, alice, bob string, id []byte)
		code   codes.Code
		reason string
	}{
		{"выход", func(t *testing.T, h *harness, _, bob string, _ []byte) {
			if _, err := h.accounts.Logout(auth(bob), &apiv1.LogoutRequest{}); err != nil {
				t.Fatal(err)
			}
		}, codes.Unauthenticated, ReasonUnauthenticated},
		{"отключение", func(t *testing.T, h *harness, _, bob string, id []byte) {
			if _, err := h.chats.DisconnectChat(auth(bob), &apiv1.ChatRef{ChatId: id}); err != nil {
				t.Fatal(err)
			}
		}, codes.FailedPrecondition, ReasonNotConnected},
		{"закрытие собеседником", func(t *testing.T, h *harness, alice, _ string, id []byte) {
			if _, err := h.chats.CloseChat(auth(alice), &apiv1.ChatRef{ChatId: id}); err != nil {
				t.Fatal(err)
			}
		}, codes.FailedPrecondition, ReasonChatClosed},
		{"удаление контакта", func(t *testing.T, h *harness, alice, _ string, _ []byte) {
			if _, err := h.contacts.RemoveContact(auth(alice), &apiv1.ContactLogin{Login: "bob"}); err != nil {
				t.Fatal(err)
			}
		}, codes.FailedPrecondition, ReasonChatClosed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := startWith(t, nil, opts, 0)
			alice, bob, id := readyChat(t, h)
			stream := h.openStream(t, bob, id)
			c.action(t, h, alice, bob, id)
			err, took := stream.ends(limit + 2*time.Second)
			expectError(t, err, c.code, c.reason)
			t.Logf("SC-006: %s, завершение за %v", c.name, took)
			if took > limit {
				t.Errorf("SC-006: завершение заняло %v, предел %v", took, limit)
			}
			if peerConnected(t, h, alice, id) {
				t.Error("подключение не снято")
			}
		})
	}
}

func TestSessionExpiryEndsStream(t *testing.T) {
	h := startWith(t, nil, shortOptions(), 0)
	alice, bob, id := readyChat(t, h)
	u, err := h.deps.Accounts.Authenticate(bg, bob)
	if err != nil {
		t.Fatal(err)
	}
	// Сессия с близким сроком: время идёт на самом деле, проверка у сервера та же.
	const token = "short-lived-session-token"
	sum := sessionHash(token)
	if err := h.store.CreateSession(bg, sum, u.ID, time.Now().Add(200*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	stream := h.openStream(t, token, id)
	err, took := stream.ends(3 * time.Second)
	expectError(t, err, codes.Unauthenticated, ReasonUnauthenticated)
	t.Logf("истечение сессии: завершение за %v", took)
	if peerConnected(t, h, alice, id) {
		t.Error("подключение не снято")
	}
}

func TestSecondStreamReplacesFirst(t *testing.T) {
	h := startWith(t, nil, shortOptions(), 0)
	alice, bob, id := readyChat(t, h)
	if err := h.sendFragment(alice, id, []byte("неподтверждённый")); err != nil {
		t.Fatal(err)
	}
	first := h.openStream(t, bob, id)
	_ = first.next()
	second := h.openStream(t, bob, id)
	err, _ := first.ends(3 * time.Second)
	expectError(t, err, codes.Aborted, ReasonStreamReplaced)
	got := second.next()
	if string(got.GetFragment()) != "неподтверждённый" || !got.GetRedelivered() {
		t.Fatalf("второму потоку: %q, %v", got.GetFragment(), got.GetRedelivered())
	}
	if !peerConnected(t, h, alice, id) {
		t.Fatal("замена потока сняла подключение")
	}
}

func TestClientGoneDisconnects(t *testing.T) {
	h := start(t, nil)
	alice, bob, id := readyChat(t, h)
	stream := h.openStream(t, bob, id)
	stream.cancel()
	eventually(t, "снятие подключения", func() bool { return !peerConnected(t, h, alice, id) })

	// Закрытие своей стороны потока даёт тот же результат.
	stream = h.openStream(t, bob, id)
	if !peerConnected(t, h, alice, id) {
		t.Fatal("новый поток не подключил участника")
	}
	if err := stream.s.CloseSend(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "снятие подключения", func() bool { return !peerConnected(t, h, alice, id) })
}

func TestInvalidSequence(t *testing.T) {
	h := start(t, nil, func(d *Deps) { d.OpenTimeout = 100 * time.Millisecond })
	alice, bob, id := readyChat(t, h)

	// Подтверждение до открытия: подключение не меняется.
	if _, err := h.chats.DisconnectChat(auth(bob), &apiv1.ChatRef{ChatId: id}); err != nil {
		t.Fatal(err)
	}
	stream := h.rawStream(t, bob)
	stream.ack(1)
	err, _ := stream.ends(3 * time.Second)
	expectError(t, err, codes.InvalidArgument, ReasonInvalidStreamRequest)
	if peerConnected(t, h, alice, id) {
		t.Error("поток без открытия подключил участника")
	}

	// Поток без open завершается по сроку, подключение не меняется.
	stream = h.rawStream(t, alice)
	err, took := stream.ends(3 * time.Second)
	expectError(t, err, codes.InvalidArgument, ReasonInvalidStreamRequest)
	if took > 2*time.Second {
		t.Errorf("срок первого сообщения: %v", took)
	}
	if !selfConnected(t, h, alice, id) {
		t.Error("поток без открытия снял подключение")
	}

	// Второе открытие и сообщение без типа снимают подключение.
	for name, msg := range map[string]*apiv1.ReceiveRequest{"второй open": openMessage(id), "без типа": {}} {
		stream = h.openStream(t, bob, id)
		if err := stream.s.Send(msg); err != nil {
			t.Fatal(err)
		}
		err, _ = stream.ends(3 * time.Second)
		expectError(t, err, codes.InvalidArgument, ReasonInvalidStreamRequest)
		eventually(t, name+": снятие подключения", func() bool { return !peerConnected(t, h, alice, id) })
	}
}

func TestAckTimeout(t *testing.T) {
	h := startWith(t, nil, shortOptions(), 0)
	alice, bob, id := readyChat(t, h)
	if err := h.sendFragment(alice, id, []byte("без подтверждения")); err != nil {
		t.Fatal(err)
	}
	stream := h.openStream(t, bob, id)
	_ = stream.next()
	err, _ := stream.ends(3 * time.Second)
	expectError(t, err, codes.DeadlineExceeded, ReasonAckTimeout)
	if peerConnected(t, h, alice, id) {
		t.Error("подключение не снято")
	}
	// Фрагмент остался в очереди.
	again := h.openStream(t, bob, id)
	if got := again.next(); !got.GetRedelivered() {
		t.Fatal("фрагмент не вернулся")
	}
}

// Клиент, который не читает поток, не мешает завершению и не копит горутины.
func TestNonReadingClient(t *testing.T) {
	before := runtime.NumGoroutine()
	func() {
		opts := shortOptions()
		h := startWith(t, nil, opts, 0)
		alice, bob, id := readyChat(t, h)
		stream := h.openStream(t, bob, id)
		// Наибольшие фрагменты быстро заполняют окно транспорта.
		for i := 0; i < 40; i++ {
			if err := h.sendFragment(alice, id, make([]byte, delivery.MaxFragmentSize)); err != nil {
				t.Fatal(err)
			}
		}
		eventually(t, "завершение по сроку и снятие подключения", func() bool { return !peerConnected(t, h, alice, id) })
		stream.cancel()
		h.service.Shutdown()
		h.server.GRPC.Stop()
		_ = h.conn.Close()
	}()
	eventually(t, "горутины вернулись к исходному числу", func() bool {
		runtime.GC()
		return runtime.NumGoroutine() <= before+2
	})
}

// fakeStream для прямого вызова обработчика: Send паникует.
type panicStream struct {
	grpc.BidiStreamingServer[apiv1.ReceiveRequest, apiv1.ReceiveEvent]
	ctx  context.Context
	recv chan *apiv1.ReceiveRequest
}

func (s *panicStream) Context() context.Context { return s.ctx }

func (s *panicStream) Recv() (*apiv1.ReceiveRequest, error) {
	select {
	case m := <-s.recv:
		return m, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

func (s *panicStream) Send(*apiv1.ReceiveEvent) error { panic("сбой записи") }

// Паника в горутине записи завершает поток внутренней ошибкой, а не процесс.
func TestWriterPanicIsRecovered(t *testing.T) {
	h := start(t, nil)
	_, bob, id := readyChat(t, h)
	u, err := h.deps.Accounts.Authenticate(bg, bob)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.WithValue(bg, identityKey{}, identity{user: u, token: bob}))
	defer cancel()
	stream := &panicStream{ctx: ctx, recv: make(chan *apiv1.ReceiveRequest, 1)}
	stream.recv <- openMessage(id)
	handler := &deliveryHandler{svc: h.service, accounts: h.deps.Accounts, logger: h.deps.Logger}
	err = handler.ReceiveFragments(stream)
	if status.Code(err) != codes.Internal {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), "сбой записи") || strings.Contains(h.log.String(), "сбой записи") {
		t.Error("значение паники раскрыто")
	}
}

// failingState отвечает ошибкой хранилища на сверке, пока флаг поднят.
func TestStoreFailureDuringCheck(t *testing.T) {
	failing := &flakyDelivery{}
	h := startFull(t, nil, testConfig{opt: shortOptions(), wrap: func(s store.DeliveryStore) store.DeliveryStore {
		failing.DeliveryStore = s
		return failing
	}})
	alice, bob, id := readyChat(t, h)
	stream := h.openStream(t, bob, id)
	failing.set(store.ErrUnavailable)
	err, _ := stream.ends(3 * time.Second)
	expectError(t, err, codes.Unavailable, ReasonStoreUnavailable)
	failing.set(nil)
	if !peerConnected(t, h, alice, id) {
		t.Error("сбой хранилища снял подключение")
	}
}

// Заполненная очередь и ограничение выдачи на поток.
func TestQueueFullThroughServer(t *testing.T) {
	h := startWith(t, nil, delivery.Options{AckTimeout: time.Hour}, 20)
	alice, bob, id := readyChat(t, h)
	for i := 0; i < 20; i++ {
		if err := h.sendFragment(alice, id, []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	expectError(t, h.sendFragment(alice, id, []byte("лишний")), codes.ResourceExhausted, ReasonQueueFull)

	// Клиент не подтверждает: выдано не больше 16.
	stream := h.openStream(t, bob, id)
	seen := map[byte]bool{}
	ids := []uint64{}
	for i := 0; i < delivery.Prefetch; i++ {
		d := stream.next()
		seen[d.GetFragment()[0]] = true
		ids = append(ids, d.GetDeliveryId())
	}
	extra := make(chan *apiv1.ReceiveEvent, 1)
	go func() {
		if ev, err := stream.s.Recv(); err == nil {
			extra <- ev
		}
	}()
	select {
	case ev := <-extra:
		t.Fatalf("выдан фрагмент сверх предела: %v", ev)
	case <-time.After(100 * time.Millisecond):
	}
	for _, n := range ids {
		stream.ack(n)
	}
	for i := 0; i < 4; i++ {
		var ev *apiv1.ReceiveEvent
		if i == 0 {
			ev = <-extra
		} else {
			var err error
			if ev, err = stream.s.Recv(); err != nil {
				t.Fatal(err)
			}
		}
		seen[ev.GetFragment().GetFragment()[0]] = true
		stream.ack(ev.GetFragment().GetDeliveryId())
	}
	if len(seen) != 20 {
		t.Fatalf("принятых фрагментов потеряно: получено %d", len(seen))
	}
	eventually(t, "отправка снова работает", func() bool { return h.sendFragment(alice, id, []byte("снова")) == nil })
}

// US4: сбой брокера.

func TestBrokerDownThroughServer(t *testing.T) {
	h := startWith(t, nil, shortOptions(), 0)
	alice, bob, id := readyChat(t, h)
	if err := h.sendFragment(alice, id, []byte("ждёт")); err != nil {
		t.Fatal(err)
	}
	stream := h.openStream(t, bob, id)
	first := stream.next()
	_ = first

	h.broker.SetDown(true)
	err, _ := stream.ends(3 * time.Second)
	expectError(t, err, codes.Unavailable, ReasonBrokerUnavailable)
	if !peerConnected(t, h, alice, id) {
		t.Error("сбой брокера снял подключение")
	}
	expectError(t, h.sendFragment(alice, id, []byte("x")), codes.Unavailable, ReasonBrokerUnavailable)
	opening := h.rawStream(t, bob)
	if err := opening.s.Send(openMessage(id)); err != nil {
		t.Fatal(err)
	}
	err, _ = opening.ends(3 * time.Second)
	expectError(t, err, codes.Unavailable, ReasonBrokerUnavailable)

	// Вход, контакты, чаты и обмен значениями работают.
	if _, err := h.accounts.Login(bg, &apiv1.LoginRequest{Login: "alice", Password: "password-for-tests"}); err != nil {
		t.Errorf("вход: %v", err)
	}
	if _, err := h.contacts.ListContacts(auth(alice), &apiv1.ListContactsRequest{}); err != nil {
		t.Errorf("контакты: %v", err)
	}
	if _, err := h.chats.GetChat(auth(alice), &apiv1.ChatRef{ChatId: id}); err != nil {
		t.Errorf("чат: %v", err)
	}
	if _, err := h.keys.GetPeerPublicValue(auth(alice), &apiv1.ChatRef{ChatId: id}); err != nil {
		t.Errorf("значение: %v", err)
	}

	h.broker.SetDown(false)
	again := h.openStream(t, bob, id)
	if d := again.next(); string(d.GetFragment()) != "ждёт" || !d.GetRedelivered() {
		t.Fatalf("после возврата: %q, %v", d.GetFragment(), d.GetRedelivered())
	}
	if err := h.sendFragment(alice, id, []byte("после сбоя")); err != nil {
		t.Fatal(err)
	}
	if d := again.next(); string(d.GetFragment()) != "после сбоя" {
		t.Fatalf("%q", d.GetFragment())
	}
}

// Остановка с открытым потоком укладывается в предел и не снимает подключение.
func TestShutdownWithOpenStream(t *testing.T) {
	h := start(t, nil)
	alice, bob, id := readyChat(t, h)
	stream := h.openStream(t, bob, id)
	begin := time.Now()
	h.service.Shutdown()
	stopped := make(chan struct{})
	go func() {
		h.server.GRPC.GracefulStop()
		close(stopped)
	}()
	err, _ := stream.ends(3 * time.Second)
	expectError(t, err, codes.Unavailable, ReasonServerStopping)
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("GracefulStop не уложился в 10 с")
	}
	t.Logf("остановка заняла %v", time.Since(begin))
	u, err := h.deps.Accounts.Authenticate(bg, alice)
	if err != nil {
		t.Fatal(err)
	}
	var chatID [16]byte
	copy(chatID[:], id)
	c, err := h.store.GetChat(bg, chatID, u.ID)
	if err != nil || !c.Peer.Connected {
		t.Fatalf("остановка сняла подключение: %+v, %v", c.Peer, err)
	}
}
