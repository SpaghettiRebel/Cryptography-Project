package delivery_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"cryptochat/server/internal/accounts"
	"cryptochat/server/internal/delivery"
	"cryptochat/server/internal/store"
)

func shortChecks() delivery.Options {
	return delivery.Options{AckTimeout: time.Hour, CheckInterval: 2 * time.Millisecond, CleanupInterval: 2 * time.Millisecond}
}

var errClientGone = errors.New("клиент ушёл")

// Все строки таблицы завершения потока, кроме замены и остановки: для них отдельные проверки ниже.
func TestStreamEndRules(t *testing.T) {
	cases := []struct {
		name         string
		act          func(f *fixture, sub *delivery.Subscription)
		want         error
		stillConnect bool
		failSession  bool
		failState    bool
	}{
		{"обрыв клиента", func(f *fixture, sub *delivery.Subscription) { sub.Close(errClientGone) }, errClientGone, false, false, false},
		{"отмена клиентом", func(f *fixture, sub *delivery.Subscription) { sub.Close(context.Canceled) }, context.Canceled, false, false, false},
		{"неверное сообщение", func(f *fixture, sub *delivery.Subscription) { sub.Close(delivery.ErrInvalidStreamRequest) }, delivery.ErrInvalidStreamRequest, false, false, false},
		{"внутренняя ошибка", func(f *fixture, sub *delivery.Subscription) { sub.Close(errors.New("паника")) }, nil, false, false, false},
		{"выход", func(f *fixture, sub *delivery.Subscription) {}, accounts.ErrUnauthenticated, false, true, false},
		{"отключение", func(f *fixture, sub *delivery.Subscription) {
			if _, err := f.store.SetConnected(bg, f.id, f.bob.ID, false); err != nil {
				f.t.Error(err)
			}
		}, store.ErrNotConnected, false, false, false},
		{"закрытие чата", func(f *fixture, sub *delivery.Subscription) {
			if _, err := f.store.CloseChat(bg, f.id, f.alice.ID); err != nil {
				f.t.Error(err)
			}
		}, store.ErrChatClosed, false, false, false},
		{"удаление контакта", func(f *fixture, sub *delivery.Subscription) {
			if err := f.store.RemoveContact(bg, f.alice.ID, "bob"); err != nil {
				f.t.Error(err)
			}
		}, store.ErrChatClosed, false, false, false},
		{"брокер недоступен", func(f *fixture, sub *delivery.Subscription) { f.broker.SetDown(true) }, delivery.ErrBrokerUnavailable, true, false, false},
		{"хранилище недоступно", func(f *fixture, sub *delivery.Subscription) {}, store.ErrUnavailable, true, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, shortChecks(), 0)
			var mu sync.Mutex
			sessionErr := error(nil)
			check := func(context.Context) error {
				mu.Lock()
				defer mu.Unlock()
				return sessionErr
			}
			sub, err := f.svc.Open(bg, f.bob.ID, f.id[:], check)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case c.failSession:
				mu.Lock()
				sessionErr = accounts.ErrUnauthenticated
				mu.Unlock()
			case c.failState:
				mu.Lock()
				sessionErr = store.ErrUnavailable
				mu.Unlock()
			}
			c.act(f, sub)
			got := waitDone(t, sub)
			if c.want != nil && !errors.Is(got, c.want) {
				t.Fatalf("причина %v, ожидалась %v", got, c.want)
			}
			if !c.stillConnect {
				waitFor(t, "снятие подключения", func() bool { return !f.connected(f.bob) })
			} else {
				time.Sleep(10 * time.Millisecond)
				if !f.connected(f.bob) {
					t.Fatal("подключение снято")
				}
			}
			if _, ok := <-sub.Deliveries(); ok {
				t.Fatal("канал выдач не закрыт")
			}
		})
	}
}

func TestAckTimeoutRule(t *testing.T) {
	f := newFixture(t, delivery.Options{AckTimeout: 40 * time.Millisecond, CheckInterval: time.Hour}, 0)
	f.send("a")
	sub := f.open(f.bob)
	next(t, sub)
	if err := waitDone(t, sub); !errors.Is(err, delivery.ErrAckTimeout) {
		t.Fatalf("%v", err)
	}
	waitFor(t, "снятие подключения", func() bool { return !f.connected(f.bob) })
	// Фрагмент остался в очереди.
	if n, _ := f.broker.QueueLen(delivery.QueueName(f.id, f.bob.ID)); n != 1 {
		t.Fatalf("в очереди %d", n)
	}
}

// Срок идёт от самого старого неподтверждённого: свежие подтверждения его не сбрасывают.
func TestAckTimeoutOldestWins(t *testing.T) {
	f := newFixture(t, delivery.Options{AckTimeout: 120 * time.Millisecond, CheckInterval: time.Hour}, 0)
	f.send("старый")
	sub := f.open(f.bob)
	next(t, sub)
	stop := time.After(70 * time.Millisecond)
loop:
	for {
		select {
		case <-stop:
			break loop
		case <-time.After(10 * time.Millisecond):
			f.send("свежий")
			sub.Ack(next(t, sub).ID)
		}
	}
	if err := waitDone(t, sub); !errors.Is(err, delivery.ErrAckTimeout) {
		t.Fatalf("%v", err)
	}
}

// Клиент подтверждает вовремя: поток живёт дольше срока.
func TestAckInTimeKeepsStream(t *testing.T) {
	f := newFixture(t, delivery.Options{AckTimeout: 60 * time.Millisecond, CheckInterval: time.Hour}, 0)
	sub := f.open(f.bob)
	for i := 0; i < 8; i++ {
		f.send("x")
		sub.Ack(next(t, sub).ID)
		time.Sleep(15 * time.Millisecond)
	}
	select {
	case <-sub.Done():
		t.Fatalf("поток завершён: %v", sub.Err())
	default:
	}
}

func TestSecondStreamReplaces(t *testing.T) {
	f := newFixture(t, shortChecks(), 0)
	f.send("a")
	first := f.open(f.bob)
	next(t, first)
	second := f.open(f.bob)
	if err := waitDone(t, first); !errors.Is(err, delivery.ErrStreamReplaced) {
		t.Fatalf("%v", err)
	}
	d := next(t, second)
	if string(d.Body) != "a" || !d.Redelivered {
		t.Fatalf("%q, %v", d.Body, d.Redelivered)
	}
	if !f.connected(f.bob) {
		t.Fatal("замена сняла подключение")
	}
	// Потоки другого участника и другого чата не затронуты.
	alice := f.open(f.alice)
	third := f.open(f.bob)
	if err := waitDone(t, second); !errors.Is(err, delivery.ErrStreamReplaced) {
		t.Fatalf("%v", err)
	}
	select {
	case <-alice.Done():
		t.Fatalf("чужой поток завершён: %v", alice.Err())
	case <-time.After(10 * time.Millisecond):
	}
	_ = third
}

// Закрытие старого потока одновременно с открытием нового не снимает подключение нового.
func TestCloseRacesWithOpen(t *testing.T) {
	f := newFixture(t, delivery.Options{AckTimeout: time.Hour, CheckInterval: time.Hour}, 0)
	rounds := 200
	if testing.Short() {
		rounds = 50
	}
	sub := f.open(f.bob)
	for i := 0; i < rounds; i++ {
		var wg sync.WaitGroup
		wg.Add(2)
		old := sub
		var opened *delivery.Subscription
		go func() {
			defer wg.Done()
			old.Close(errClientGone)
		}()
		go func() {
			defer wg.Done()
			var err error
			opened, err = f.svc.Open(bg, f.bob.ID, f.id[:], noSession)
			if err != nil {
				t.Error(err)
			}
		}()
		wg.Wait()
		if opened == nil {
			t.FailNow()
		}
		waitDone(t, old)
		if !f.connected(f.bob) {
			t.Fatalf("круг %d: новый поток остался без подключения", i)
		}
		sub = opened
	}
	sub.Close(errClientGone)
}

// Снятие подключения выполняется и при уже отменённом контексте потока.
func TestDisconnectAfterContextCancel(t *testing.T) {
	f := newFixture(t, shortChecks(), 0)
	ctx, cancel := context.WithCancel(bg)
	sub, err := f.svc.Open(ctx, f.bob.ID, f.id[:], noSession)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	sub.Close(context.Canceled)
	waitFor(t, "снятие подключения", func() bool { return !f.connected(f.bob) })
}

type failingChats struct {
	store.ChatStore
	fail bool
}

func (c *failingChats) SetConnected(ctx context.Context, id [16]byte, user string, connected bool) (store.Chat, error) {
	if !connected && c.fail {
		return store.Chat{}, store.ErrUnavailable
	}
	return c.ChatStore.SetConnected(ctx, id, user, connected)
}

// Ошибка записи снятия не паникует и оставляет подключение установленным.
func TestDisconnectFailureIsHarmless(t *testing.T) {
	f := newFixture(t, shortChecks(), 0)
	chats := &failingChats{ChatStore: f.store, fail: true}
	svc := delivery.New(f.count, chats, f.broker, nil, shortChecks())
	sub, err := svc.Open(bg, f.bob.ID, f.id[:], noSession)
	if err != nil {
		t.Fatal(err)
	}
	sub.Close(errClientGone)
	waitDone(t, sub)
	time.Sleep(10 * time.Millisecond)
	if !f.connected(f.bob) {
		t.Fatal("подключение снято")
	}
}

func TestShutdownEndsStreams(t *testing.T) {
	f := newFixture(t, shortChecks(), 0)
	bob := f.open(f.bob)
	alice := f.open(f.alice)
	f.svc.Shutdown()
	for _, sub := range []*delivery.Subscription{bob, alice} {
		if err := waitDone(t, sub); !errors.Is(err, delivery.ErrStopping) {
			t.Fatalf("%v", err)
		}
	}
	if !f.connected(f.bob) || !f.connected(f.alice) {
		t.Fatal("остановка сняла подключение")
	}
	if _, err := f.svc.Open(bg, f.bob.ID, f.id[:], noSession); !errors.Is(err, delivery.ErrStopping) {
		t.Fatalf("Open после остановки: %v", err)
	}
}

func TestOpenRules(t *testing.T) {
	f := newFixture(t, shortChecks(), 0)
	if _, err := f.svc.Open(bg, f.bob.ID, []byte{1}, noSession); err == nil {
		t.Error("неверный идентификатор")
	}
	if _, err := f.svc.Open(bg, "посторонний", f.id[:], noSession); !errors.Is(err, store.ErrChatNotFound) {
		t.Errorf("посторонний: %v", err)
	}
	if q := f.broker.Queues(); len(q) != 0 {
		t.Errorf("очереди: %v", q)
	}
	// Открытие подключает участника.
	if _, err := f.store.SetConnected(bg, f.id, f.bob.ID, false); err != nil {
		t.Fatal(err)
	}
	f.open(f.bob)
	if !f.connected(f.bob) {
		t.Error("открытие не подключило")
	}
	if _, err := f.store.CloseChat(bg, f.id, f.alice.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Open(bg, f.alice.ID, f.id[:], noSession); !errors.Is(err, store.ErrChatClosed) {
		t.Errorf("закрытый чат: %v", err)
	}
}

// Закрытие внутри объявления очереди при открытии: очередь удаляется, ответ — чат закрыт.
func TestOpenRaceWithClose(t *testing.T) {
	f := newFixture(t, shortChecks(), 0)
	f.broker.beforeEnsure = func() {
		if _, err := f.store.CloseChat(bg, f.id, f.alice.ID); err != nil {
			t.Error(err)
		}
	}
	if _, err := f.svc.Open(bg, f.bob.ID, f.id[:], noSession); !errors.Is(err, store.ErrChatClosed) {
		t.Fatalf("%v", err)
	}
	if q := f.broker.Queues(); len(q) != 0 {
		t.Fatalf("очереди: %v", q)
	}
}

// Брокер недоступен при открытии: отдельная причина; после возврата поток открывается без перезапуска.
func TestOpenBrokerDown(t *testing.T) {
	f := newFixture(t, shortChecks(), 0)
	f.broker.SetDown(true)
	if _, err := f.svc.Open(bg, f.bob.ID, f.id[:], noSession); !errors.Is(err, delivery.ErrBrokerUnavailable) {
		t.Fatalf("%v", err)
	}
	f.broker.SetDown(false)
	f.open(f.bob)
}

// Неудачное открытие не меняет подключение и не трогает работающий поток.
func TestOpenFailureChangesNothing(t *testing.T) {
	failed := func(t *testing.T, f *fixture, ctx context.Context, want error) {
		t.Helper()
		if _, err := f.svc.Open(ctx, f.bob.ID, f.id[:], noSession); !errors.Is(err, want) {
			t.Fatalf("%v", err)
		}
	}
	connected := func(t *testing.T, f *fixture) bool {
		t.Helper()
		st, err := f.store.DeliveryState(bg, f.id, f.bob.ID)
		if err != nil {
			t.Fatal(err)
		}
		return st.Connected
	}
	disconnected := func(t *testing.T) *fixture {
		t.Helper()
		f := newFixture(t, shortChecks(), 0)
		if _, err := f.store.SetConnected(bg, f.id, f.bob.ID, false); err != nil {
			t.Fatal(err)
		}
		return f
	}
	brokerDown := delivery.ErrBrokerUnavailable

	t.Run("брокер недоступен, участник не был подключён", func(t *testing.T) {
		f := disconnected(t)
		f.broker.SetDown(true)
		failed(t, f, bg, delivery.ErrBrokerUnavailable)
		if connected(t, f) {
			t.Fatal("подключение установлено")
		}
	})
	t.Run("подписка не создана, участник не был подключён", func(t *testing.T) {
		f := disconnected(t)
		f.broker.failConsume.Store(&brokerDown)
		failed(t, f, bg, delivery.ErrBrokerUnavailable)
		if connected(t, f) {
			t.Fatal("подключение установлено")
		}
	})
	t.Run("отмена вызова", func(t *testing.T) {
		f := disconnected(t)
		ctx, cancel := context.WithCancel(bg)
		cancel()
		if _, err := f.svc.Open(ctx, f.bob.ID, f.id[:], noSession); err == nil {
			t.Fatal("открытие с отменённым контекстом прошло")
		}
		if connected(t, f) {
			t.Fatal("подключение установлено")
		}
	})
	t.Run("работающий поток остаётся", func(t *testing.T) {
		f := newFixture(t, shortChecks(), 0)
		old := f.open(f.bob)
		f.broker.failConsume.Store(&brokerDown)
		failed(t, f, bg, delivery.ErrBrokerUnavailable)
		if !connected(t, f) {
			t.Fatal("подключение снято")
		}
		select {
		case <-old.Done():
			t.Fatalf("старый поток завершён: %v", old.Err())
		default:
		}
		f.send("после сбоя")
		select {
		case d := <-old.Deliveries():
			if string(d.Body) != "после сбоя" {
				t.Fatalf("%q", d.Body)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("старый поток не получил фрагмент")
		}
	})
	t.Run("после успешного открытия подключён", func(t *testing.T) {
		f := disconnected(t)
		f.open(f.bob)
		if !connected(t, f) {
			t.Fatal("подключения нет")
		}
	})
}

// Фрагмент, ждущий за другими, срок не расходует: считается только выданный клиенту.
func TestAckTimeoutCountsFromIssue(t *testing.T) {
	const limit = 300 * time.Millisecond
	f := newFixture(t, delivery.Options{AckTimeout: limit, CheckInterval: time.Hour, CleanupInterval: time.Hour}, 0)
	f.send("первый")
	f.send("второй")
	sub := f.open(f.bob)
	// Клиент не спешит, но каждый фрагмент подтверждает в срок: суммарно дольше предела.
	for i := 0; i < 2; i++ {
		time.Sleep(limit * 2 / 3)
		select {
		case d := <-sub.Deliveries():
			sub.Ack(d.ID)
		case <-sub.Done():
			t.Fatalf("фрагмент %d: поток завершён: %v", i, sub.Err())
		}
	}
	select {
	case <-sub.Done():
		t.Fatalf("поток завершён: %v", sub.Err())
	case <-time.After(limit + limit/2):
	}
}
