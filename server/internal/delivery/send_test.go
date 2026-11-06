package delivery_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"cryptochat/server/internal/chats"
	"cryptochat/server/internal/delivery"
	"cryptochat/server/internal/store"
)

func TestSendChecksInOrder(t *testing.T) {
	f := newFixture(t, delivery.Options{}, 0)
	ok := []byte("x")
	cases := []struct {
		name     string
		fragment []byte
		id       []byte
		user     string
		want     error
	}{
		{"пустой", nil, f.id[:], f.alice.ID, delivery.ErrInvalidFragment},
		{"пустой при неверном идентификаторе", []byte{}, []byte{1}, f.alice.ID, delivery.ErrInvalidFragment},
		{"больше предела", make([]byte, 61441), f.id[:], f.alice.ID, delivery.ErrFragmentTooLarge},
		{"больше предела раньше проверки чата", make([]byte, 61441), make([]byte, 16), "посторонний", delivery.ErrFragmentTooLarge},
		{"неверный идентификатор", ok, []byte{1, 2, 3}, f.alice.ID, chats.ErrInvalidChatID},
		{"посторонний", ok, f.id[:], "посторонний", store.ErrChatNotFound},
		{"нет такого чата", ok, make([]byte, 16), f.alice.ID, store.ErrChatNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := f.svc.Send(bg, c.user, c.id, c.fragment); !errors.Is(err, c.want) {
				t.Fatalf("%v, ожидалось %v", err, c.want)
			}
		})
	}
	if q := f.broker.Queues(); len(q) != 0 {
		t.Fatalf("неверные фрагменты создали очереди: %v", q)
	}
	if err := f.svc.Send(bg, f.alice.ID, f.id[:], make([]byte, 61440)); err != nil {
		t.Fatalf("наибольший фрагмент: %v", err)
	}
}

func TestSendRequiresConnectedReadyChat(t *testing.T) {
	f := newFixture(t, delivery.Options{}, 0)
	before := f.broker.publishes.Load()

	notReady := f.newChat()
	if err := f.svc.Send(bg, f.alice.ID, notReady[:], []byte("x")); !errors.Is(err, delivery.ErrChatNotReady) {
		t.Errorf("не готов: %v", err)
	}
	if _, err := f.store.SetConnected(bg, f.id, f.alice.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Send(bg, f.alice.ID, f.id[:], []byte("x")); !errors.Is(err, store.ErrNotConnected) {
		t.Errorf("не подключён: %v", err)
	}
	if _, err := f.store.CloseChat(bg, f.id, f.bob.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Send(bg, f.alice.ID, f.id[:], []byte("x")); !errors.Is(err, store.ErrChatClosed) {
		t.Errorf("закрыт: %v", err)
	}
	if f.broker.publishes.Load() != before {
		t.Error("отвергнутый фрагмент дошёл до брокера")
	}
}

// Собеседник не подключён: это обычный случай асинхронного обмена.
func TestSendToDisconnectedPeer(t *testing.T) {
	f := newFixture(t, delivery.Options{}, 0)
	if _, err := f.store.SetConnected(bg, f.id, f.bob.ID, false); err != nil {
		t.Fatal(err)
	}
	f.send("ждёт")
	if n, ok := f.broker.QueueLen(delivery.QueueName(f.id, f.bob.ID)); !ok || n != 1 {
		t.Fatalf("очередь получателя: %d, %v", n, ok)
	}
}

func TestSendBodyAndQueues(t *testing.T) {
	f := newFixture(t, delivery.Options{}, 0)
	body := []byte{0, 1, 2, 0xFF, 0xFE, 'x'}
	want := append([]byte(nil), body...)
	if err := f.svc.Send(bg, f.alice.ID, f.id[:], body); err != nil {
		t.Fatal(err)
	}
	body[0] = 0x55
	bobQueue := delivery.QueueName(f.id, f.bob.ID)
	got := f.broker.Bodies(bobQueue)
	if len(got) != 1 || !bytes.Equal(got[0], want) {
		t.Fatalf("тело в брокере: %x", got)
	}
	// Отправитель в очереди получателя не появляется, своя очередь не создаётся.
	if _, ok := f.broker.QueueLen(delivery.QueueName(f.id, f.alice.ID)); ok {
		t.Error("создана очередь отправителя")
	}
}

// Объявление один раз, а на обычный фрагмент ровно одно чтение состояния.
func TestSendIsLight(t *testing.T) {
	f := newFixture(t, delivery.Options{}, 0)
	f.send("первый")
	if n := f.broker.ensures.Load(); n != 1 {
		t.Fatalf("объявлений %d", n)
	}
	f.count.states.Store(0)
	for i := 0; i < 5; i++ {
		f.send("обычный")
	}
	if n := f.broker.ensures.Load(); n != 1 {
		t.Errorf("объявлений %d", n)
	}
	if n := f.count.states.Load(); n != 5 {
		t.Errorf("чтений состояния %d на 5 фрагментов", n)
	}
}

// Запрос, отменённый клиентом, даёт обычную ошибку, а не недоступность брокера.
func TestSendClientCancel(t *testing.T) {
	f := newFixture(t, delivery.Options{}, 0)
	ctx, cancel := context.WithCancel(bg)
	f.broker.beforePublish = cancel
	err := f.svc.Send(ctx, f.alice.ID, f.id[:], []byte("x"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

type stuckBroker struct{ *hookBroker }

func (s stuckBroker) Publish(ctx context.Context, _ string, _ []byte) error {
	<-ctx.Done()
	return ctx.Err()
}

// Истечение общего предела работы с брокером означает, что брокер недоступен.
func TestSendBrokerTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("настоящий предел 5 с")
	}
	f := newFixture(t, delivery.Options{}, 0)
	svc := delivery.New(f.count, f.store, stuckBroker{f.broker}, nil, delivery.Options{})
	start := time.Now()
	err := svc.Send(bg, f.alice.ID, f.id[:], []byte("x"))
	if !errors.Is(err, delivery.ErrBrokerUnavailable) {
		t.Fatalf("%v", err)
	}
	if took := time.Since(start); took < delivery.PublishTimeout-time.Second || took > delivery.PublishTimeout+time.Second {
		t.Fatalf("ответ за %v", took)
	}
}

// Заполненная очередь даёт отдельную ошибку, ничего не теряет и освобождается после приёма.
func TestQueueFull(t *testing.T) {
	f := newFixture(t, delivery.Options{}, 3)
	bodies := []string{"a", "b", "c"}
	for _, b := range bodies {
		f.send(b)
	}
	if err := f.svc.Send(bg, f.alice.ID, f.id[:], []byte("d")); !errors.Is(err, delivery.ErrQueueFull) {
		t.Fatalf("%v", err)
	}
	sub := f.open(f.bob)
	for _, want := range bodies {
		d := next(t, sub)
		if string(d.Body) != want {
			t.Fatalf("%q, ожидалось %q", d.Body, want)
		}
		sub.Ack(d.ID)
	}
	f.send("d")
	if d := next(t, sub); string(d.Body) != "d" {
		t.Fatalf("%q", d.Body)
	}
}

// Сбой брокера даёт отдельную причину, а после возврата всё работает без перезапуска.
func TestSendBrokerDown(t *testing.T) {
	f := newFixture(t, delivery.Options{}, 0)
	f.send("до сбоя")
	f.broker.SetDown(true)
	start := time.Now()
	if err := f.svc.Send(bg, f.alice.ID, f.id[:], []byte("x")); !errors.Is(err, delivery.ErrBrokerUnavailable) {
		t.Fatalf("%v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("ответ за %v", took)
	}
	f.broker.SetDown(false)
	f.send("после сбоя")
	if got := f.broker.Bodies(delivery.QueueName(f.id, f.bob.ID)); len(got) != 2 {
		t.Fatalf("в очереди %d", len(got))
	}
}
