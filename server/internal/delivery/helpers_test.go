package delivery_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cryptochat/server/internal/delivery"
	"cryptochat/server/internal/delivery/membroker"
	"cryptochat/server/internal/store"
	"cryptochat/server/internal/store/memstore"
)

var bg = context.Background()

// countingStore считает вызовы DeliveryState.
type countingStore struct {
	store.DeliveryStore
	states atomic.Int64
}

func (c *countingStore) DeliveryState(ctx context.Context, id [16]byte, userID string) (store.DeliveryState, error) {
	c.states.Add(1)
	return c.DeliveryStore.DeliveryState(ctx, id, userID)
}

// hookBroker выполняет действия перед вызовами брокера и считает их.
type hookBroker struct {
	*membroker.Broker
	ensures, publishes, deletes atomic.Int64
	beforeEnsure                func()
	beforePublish               func()
	failDelete                  atomic.Pointer[func(string) error]
	failConsume                 atomic.Pointer[error]
	consumers                   sync.Map
	acks                        atomic.Int64
}

func (h *hookBroker) EnsureQueue(ctx context.Context, q string) error {
	h.ensures.Add(1)
	if h.beforeEnsure != nil {
		h.beforeEnsure()
	}
	return h.Broker.EnsureQueue(ctx, q)
}

func (h *hookBroker) Publish(ctx context.Context, q string, body []byte) error {
	h.publishes.Add(1)
	if h.beforePublish != nil {
		h.beforePublish()
	}
	return h.Broker.Publish(ctx, q, body)
}

func (h *hookBroker) DeleteQueue(ctx context.Context, q string) error {
	h.deletes.Add(1)
	if fn := h.failDelete.Load(); fn != nil {
		if err := (*fn)(q); err != nil {
			return err
		}
	}
	return h.Broker.DeleteQueue(ctx, q)
}

func (h *hookBroker) Consume(ctx context.Context, q string, prefetch int) (delivery.Consumer, error) {
	if err := h.failConsume.Load(); err != nil {
		return nil, *err
	}
	c, err := h.Broker.Consume(ctx, q, prefetch)
	if err != nil {
		return nil, err
	}
	return &countingConsumer{Consumer: c, acks: &h.acks}, nil
}

type countingConsumer struct {
	delivery.Consumer
	acks *atomic.Int64
}

func (c *countingConsumer) Ack(id uint64) error {
	c.acks.Add(1)
	return c.Consumer.Ack(id)
}

// fixture — готовый к обмену чат двух контактов.
type fixture struct {
	t      *testing.T
	store  *memstore.Store
	count  *countingStore
	broker *hookBroker
	svc    *delivery.Service
	id     [16]byte
	alice  store.User
	bob    store.User
}

func newService(f *fixture, opt delivery.Options) *delivery.Service {
	return delivery.New(f.count, f.store, f.broker, slog.New(slog.NewTextHandler(discard{}, nil)), opt)
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func newFixture(t *testing.T, opt delivery.Options, maxMessages int) *fixture {
	t.Helper()
	m := memstore.New()
	alice, err := m.CreateUser(bg, "alice", "alice", "$h")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := m.CreateUser(bg, "bob", "bob", "$h")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SendRequest(bg, alice.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := m.AcceptRequest(bg, bob.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, store: m, count: &countingStore{DeliveryStore: m}, broker: &hookBroker{Broker: membroker.New(maxMessages)}, alice: alice, bob: bob}
	f.svc = newService(f, opt)
	f.t.Cleanup(f.svc.Shutdown)
	f.id = f.newChat()
	f.ready(f.id)
	return f
}

func (f *fixture) newChat() [16]byte {
	f.t.Helper()
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.store.CreateChat(bg, id, f.alice.ID, "bob", store.ChatParams{Algorithm: 2, KeyBits: 256, CipherMode: 2, PaddingMode: 3}); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// ready подключает обоих и публикует значения.
func (f *fixture) ready(id [16]byte) {
	f.t.Helper()
	if _, err := f.store.SetConnected(bg, id, f.bob.ID, true); err != nil {
		f.t.Fatal(err)
	}
	for _, u := range []store.User{f.alice, f.bob} {
		if err := f.store.PublishValue(bg, id, u.ID, bytes.Repeat([]byte{1}, 256)); err != nil {
			f.t.Fatal(err)
		}
	}
}

func noSession(context.Context) error { return nil }

func (f *fixture) open(u store.User) *delivery.Subscription {
	f.t.Helper()
	sub, err := f.svc.Open(bg, u.ID, f.id[:], noSession)
	if err != nil {
		f.t.Fatalf("Open: %v", err)
	}
	f.t.Cleanup(func() { sub.Close(delivery.ErrStopping) })
	return sub
}

func (f *fixture) send(body string) {
	f.t.Helper()
	if err := f.svc.Send(bg, f.alice.ID, f.id[:], []byte(body)); err != nil {
		f.t.Fatalf("Send: %v", err)
	}
}

func (f *fixture) connected(u store.User) bool {
	f.t.Helper()
	c, err := f.store.GetChat(bg, f.id, u.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return c.Self.Connected
}

func next(t *testing.T, sub *delivery.Subscription) delivery.Delivery {
	t.Helper()
	select {
	case d, ok := <-sub.Deliveries():
		if !ok {
			t.Fatalf("поток завершён: %v", sub.Err())
		}
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("нет выдачи")
	}
	return delivery.Delivery{}
}

func quiet(t *testing.T, sub *delivery.Subscription) {
	t.Helper()
	select {
	case d, ok := <-sub.Deliveries():
		if ok {
			t.Fatalf("лишняя выдача %q", d.Body)
		}
		t.Fatalf("поток завершён: %v", sub.Err())
	case <-time.After(30 * time.Millisecond):
	}
}

// waitDone ждёт завершения потока и возвращает причину.
func waitDone(t *testing.T, sub *delivery.Subscription) error {
	t.Helper()
	select {
	case <-sub.Done():
		return sub.Err()
	case <-time.After(5 * time.Second):
		t.Fatal("поток не завершился")
	}
	return nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("не дождались: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
