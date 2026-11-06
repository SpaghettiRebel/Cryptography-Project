package delivery_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"cryptochat/server/internal/delivery"
	"cryptochat/server/internal/store"
)

func fastCleanup() delivery.Options {
	return delivery.Options{CleanupInterval: 2 * time.Millisecond}
}

func (f *fixture) runCleanup(svc *delivery.Service) func() {
	f.t.Helper()
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.RunCleanup(ctx)
	}()
	stop := func() {
		cancel()
		<-done
	}
	f.t.Cleanup(stop)
	return stop
}

func (f *fixture) queues() (string, string) {
	return delivery.QueueName(f.id, f.alice.ID), delivery.QueueName(f.id, f.bob.ID)
}

func (f *fixture) noQueues() {
	f.t.Helper()
	waitFor(f.t, "удаление очередей", func() bool { return len(f.broker.Queues()) == 0 })
}

func (f *fixture) pending() int {
	f.t.Helper()
	list, err := f.store.PendingQueueCleanup(bg, 100)
	if err != nil {
		f.t.Fatal(err)
	}
	return len(list)
}

func TestCleanupAfterClose(t *testing.T) {
	f := newFixture(t, fastCleanup(), 0)
	f.send("не доставлен")
	if err := f.svc.Send(bg, f.bob.ID, f.id[:], []byte("встречный")); err != nil {
		t.Fatal(err)
	}
	if len(f.broker.Queues()) != 2 {
		t.Fatalf("очереди: %v", f.broker.Queues())
	}
	f.runCleanup(f.svc)
	if len(f.broker.Queues()) != 2 {
		t.Fatal("очереди активного чата удалены")
	}
	if _, err := f.store.CloseChat(bg, f.id, f.alice.ID); err != nil {
		t.Fatal(err)
	}
	f.noQueues()
	waitFor(t, "отметка", func() bool { return f.pending() == 0 })
}

func TestCleanupAfterRemoveContact(t *testing.T) {
	f := newFixture(t, fastCleanup(), 0)
	other := f.newChat()
	f.ready(other)
	f.send("a")
	if err := f.svc.Send(bg, f.alice.ID, other[:], []byte("b")); err != nil {
		t.Fatal(err)
	}
	if len(f.broker.Queues()) != 2 {
		t.Fatalf("очереди: %v", f.broker.Queues())
	}
	f.runCleanup(f.svc)
	if err := f.store.RemoveContact(bg, f.alice.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	f.noQueues()
	waitFor(t, "отметка", func() bool { return f.pending() == 0 })
}

// Закрытие при недоступном брокере: отметки нет, а после возврата очереди удалены.
func TestCleanupWaitsForBroker(t *testing.T) {
	f := newFixture(t, fastCleanup(), 0)
	f.send("a")
	f.broker.SetDown(true)
	f.runCleanup(f.svc)
	if _, err := f.store.CloseChat(bg, f.id, f.alice.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if f.pending() != 1 {
		t.Fatal("отметка поставлена при недоступном брокере")
	}
	f.broker.SetDown(false)
	f.noQueues()
	waitFor(t, "отметка", func() bool { return f.pending() == 0 })
}

// Сбой удаления одной очереди оставляет чат без отметки, следующий проход завершает работу.
func TestCleanupPartialFailure(t *testing.T) {
	f := newFixture(t, fastCleanup(), 0)
	f.send("a")
	aliceQueue, bobQueue := f.queues()
	if err := f.svc.Send(bg, f.bob.ID, f.id[:], []byte("b")); err != nil {
		t.Fatal(err)
	}
	fail := func(q string) error {
		if q == bobQueue {
			return delivery.ErrBrokerUnavailable
		}
		return nil
	}
	f.broker.failDelete.Store(&fail)
	if _, err := f.store.CloseChat(bg, f.id, f.alice.ID); err != nil {
		t.Fatal(err)
	}
	f.runCleanup(f.svc)
	waitFor(t, "несколько проходов", func() bool { return f.broker.deletes.Load() > 4 })
	if f.pending() != 1 {
		t.Fatal("отметка поставлена при сбое удаления")
	}
	_, _ = aliceQueue, bobQueue
	f.broker.failDelete.Store(nil)
	f.noQueues()
	waitFor(t, "отметка", func() bool { return f.pending() == 0 })
}

// Новый экземпляр сервиса завершает начатое: работа видна по хранилищу.
func TestCleanupAfterRestart(t *testing.T) {
	f := newFixture(t, fastCleanup(), 0)
	f.send("a")
	if _, err := f.store.CloseChat(bg, f.id, f.alice.ID); err != nil {
		t.Fatal(err)
	}
	restarted := newService(f, fastCleanup())
	f.runCleanup(restarted)
	f.noQueues()
	waitFor(t, "отметка", func() bool { return f.pending() == 0 })
}

// Закрытие внутри объявления очереди: она удаляется, ответ — чат закрыт.
func TestSendRaceCloseDuringEnsure(t *testing.T) {
	f := newFixture(t, fastCleanup(), 0)
	f.broker.beforeEnsure = func() {
		if _, err := f.store.CloseChat(bg, f.id, f.alice.ID); err != nil {
			t.Error(err)
		}
	}
	if err := f.svc.Send(bg, f.bob.ID, f.id[:], []byte("x")); !errors.Is(err, store.ErrChatClosed) {
		t.Fatalf("%v", err)
	}
	if q := f.broker.Queues(); len(q) != 0 {
		t.Fatalf("очереди: %v", q)
	}
	if d := f.broker.deletes.Load(); d == 0 {
		t.Fatal("сервис не удалил очередь сам")
	}
}

// Закрытие и проход обработчика внутри публикации: ответ — чат закрыт, очередей нет.
func TestSendRaceCloseDuringPublish(t *testing.T) {
	f := newFixture(t, fastCleanup(), 0)
	f.send("первый")
	f.broker.beforePublish = func() {
		if _, err := f.store.CloseChat(bg, f.id, f.alice.ID); err != nil {
			t.Error(err)
		}
		_, bobQueue := f.queues()
		if err := f.broker.Broker.DeleteQueue(bg, bobQueue); err != nil {
			t.Error(err)
		}
	}
	if err := f.svc.Send(bg, f.alice.ID, f.id[:], []byte("x")); !errors.Is(err, store.ErrChatClosed) {
		t.Fatalf("%v", err)
	}
	if q := f.broker.Queues(); len(q) != 0 {
		t.Fatalf("очереди: %v", q)
	}
}

// Закрытие после проверки, а публикация прошла раньше удаления: очередь с фрагментом удаляет обработчик.
func TestSendRaceCloseAfterPublish(t *testing.T) {
	f := newFixture(t, fastCleanup(), 0)
	f.send("первый")
	f.broker.beforePublish = func() {
		if _, err := f.store.CloseChat(bg, f.id, f.alice.ID); err != nil {
			t.Error(err)
		}
	}
	if err := f.svc.Send(bg, f.alice.ID, f.id[:], []byte("x")); err != nil {
		t.Fatalf("фрагмент был принят до удаления очереди: %v", err)
	}
	f.broker.beforePublish = nil
	f.runCleanup(f.svc)
	f.noQueues()
}

// Сбой брокера при удалении в гонке с закрытием не оставляет очереди: их добирает обработчик.
func TestSendRaceDeleteFailureRetried(t *testing.T) {
	f := newFixture(t, fastCleanup(), 0)
	stopFailing := make(chan struct{})
	fail := func(string) error {
		select {
		case <-stopFailing:
			return nil
		default:
			return delivery.ErrBrokerUnavailable
		}
	}
	f.broker.failDelete.Store(&fail)
	f.broker.beforeEnsure = func() {
		if _, err := f.store.CloseChat(bg, f.id, f.alice.ID); err != nil {
			t.Error(err)
		}
		// Обработчик успел отметить чат до появления очереди.
		if err := f.store.MarkQueuesDeleted(bg, f.id); err != nil {
			t.Error(err)
		}
	}
	if err := f.svc.Send(bg, f.alice.ID, f.id[:], []byte("x")); !errors.Is(err, store.ErrChatClosed) {
		t.Fatalf("%v", err)
	}
	if len(f.broker.Queues()) != 1 {
		t.Fatalf("очереди: %v", f.broker.Queues())
	}
	f.runCleanup(f.svc)
	close(stopFailing)
	f.noQueues()
}

// Очередь из гонки с закрытием не удалилась, а сервер перезапустился: отметка снята, новый процесс её найдёт.
func TestLateQueueSurvivesRestart(t *testing.T) {
	f := newFixture(t, fastCleanup(), 0)
	down := func(string) error { return delivery.ErrBrokerUnavailable }
	f.broker.failDelete.Store(&down)
	f.broker.beforeEnsure = func() {
		if _, err := f.store.CloseChat(bg, f.id, f.alice.ID); err != nil {
			t.Error(err)
		}
		if err := f.store.MarkQueuesDeleted(bg, f.id); err != nil {
			t.Error(err)
		}
	}
	if err := f.svc.Send(bg, f.alice.ID, f.id[:], []byte("x")); !errors.Is(err, store.ErrChatClosed) {
		t.Fatalf("%v", err)
	}
	f.broker.beforeEnsure = nil
	if f.pending() != 1 {
		t.Fatalf("чат не вернулся в список: %d", f.pending())
	}
	f.broker.failDelete.Store(nil)
	f.runCleanup(newService(f, fastCleanup()))
	f.noQueues()
	waitFor(t, "отметка", func() bool { return f.pending() == 0 })
}

// Брокер потерял очередь активного чата: она создаётся заново одной повторной публикацией.
func TestQueueLostByBroker(t *testing.T) {
	f := newFixture(t, delivery.Options{}, 0)
	f.send("первый")
	_, bobQueue := f.queues()
	if err := f.broker.Broker.DeleteQueue(bg, bobQueue); err != nil {
		t.Fatal(err)
	}
	before := f.broker.publishes.Load()
	f.send("второй")
	if n := f.broker.publishes.Load() - before; n != 2 {
		t.Fatalf("публикаций: %d", n)
	}
	got := f.broker.Bodies(bobQueue)
	if len(got) != 1 || string(got[0]) != "второй" {
		t.Fatalf("очередь: %q", got)
	}
}
