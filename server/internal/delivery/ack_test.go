package delivery_test

import (
	"fmt"
	"testing"
	"time"

	"cryptochat/server/internal/delivery"
)

// Брокер получает подтверждение только после подтверждения клиента.
func TestAckReachesBrokerOnlyFromClient(t *testing.T) {
	f := newFixture(t, delivery.Options{}, 0)
	f.send("a")
	f.send("b")
	sub := f.open(f.bob)
	a := next(t, sub)
	b := next(t, sub)
	time.Sleep(20 * time.Millisecond)
	if n := f.broker.acks.Load(); n != 0 {
		t.Fatalf("подтверждений брокеру без клиента: %d", n)
	}
	sub.Ack(a.ID)
	if n := f.broker.acks.Load(); n != 1 {
		t.Fatalf("подтверждений брокеру: %d", n)
	}
	// Неизвестный и повторный номера игнорируются, другие фрагменты не подтверждаются, поток работает.
	sub.Ack(a.ID)
	sub.Ack(9999)
	if n := f.broker.acks.Load(); n != 1 {
		t.Fatalf("лишнее подтверждение брокеру: %d", n)
	}
	queue := delivery.QueueName(f.id, f.bob.ID)
	if n, _ := f.broker.QueueLen(queue); n != 1 {
		t.Fatalf("в очереди %d", n)
	}
	f.send("c")
	c := next(t, sub)
	sub.Ack(b.ID)
	sub.Ack(c.ID)
	if n, _ := f.broker.QueueLen(queue); n != 0 {
		t.Fatalf("в очереди %d", n)
	}
}

// Неподтверждённый возвращается после закрытия, подтверждённый нет.
func TestUnackedReturns(t *testing.T) {
	f := newFixture(t, delivery.Options{}, 0)
	f.send("a")
	f.send("b")
	sub := f.open(f.bob)
	a := next(t, sub)
	next(t, sub)
	sub.Ack(a.ID)
	sub.Close(fmt.Errorf("клиент ушёл"))
	waitDone(t, sub)

	again := f.open(f.bob)
	d := next(t, again)
	if string(d.Body) != "b" || !d.Redelivered {
		t.Fatalf("повтор: %q, %v", d.Body, d.Redelivered)
	}
	again.Ack(d.ID)
	quiet(t, again)
}

// У потока не больше Prefetch неподтверждённых.
func TestPrefetchLimit(t *testing.T) {
	f := newFixture(t, delivery.Options{}, 0)
	for i := 0; i < delivery.Prefetch+5; i++ {
		f.send(fmt.Sprintf("%d", i))
	}
	sub := f.open(f.bob)
	first := next(t, sub)
	for i := 1; i < delivery.Prefetch; i++ {
		next(t, sub)
	}
	quiet(t, sub)
	sub.Ack(first.ID)
	next(t, sub)
	quiet(t, sub)
}

// Номер, ещё не выданный клиенту, нельзя подтвердить заранее.
func TestAckOfNotIssuedIgnored(t *testing.T) {
	f := newFixture(t, delivery.Options{}, 0)
	for i := 0; i < 3; i++ {
		f.send(fmt.Sprintf("%d", i))
	}
	sub := f.open(f.bob)
	waitFor(t, "выдача брокером", func() bool { n, _ := f.broker.QueueLen(delivery.QueueName(f.id, f.bob.ID)); return n == 3 })
	time.Sleep(10 * time.Millisecond)
	sub.Ack(3) // Номера последовательны, но третий ещё не выдан: до него не дошла очередь чтения.
	if n := f.broker.acks.Load(); n != 0 {
		t.Fatalf("подтверждён невыданный номер: %d", n)
	}
}
