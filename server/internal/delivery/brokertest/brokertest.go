// Пакет brokertest содержит общую проверку поведения брокера: она запускается и на брокере в памяти, и на
// настоящем RabbitMQ. В сервер не входит.
package brokertest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"cryptochat/server/internal/delivery"
)

// Factory создаёт брокер. maxMessages больше нуля задаёт меньший предел длины очереди.
type Factory func(t *testing.T, maxMessages int) delivery.Broker

const waitLimit = 10 * time.Second

// silence — сколько ждать, чтобы убедиться, что выдачи не будет.
func silence() time.Duration {
	if testing.Short() {
		return 30 * time.Millisecond
	}
	return 300 * time.Millisecond
}

type env struct {
	t *testing.T
	b delivery.Broker
}

func ctxFor(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// queue объявляет очередь с уникальным именем и удаляет её в конце проверки.
func (e *env) queue() string {
	e.t.Helper()
	name := e.name()
	if err := e.b.EnsureQueue(ctxFor(e.t), name); err != nil {
		e.t.Fatalf("EnsureQueue: %v", err)
	}
	return name
}

func (e *env) name() string {
	e.t.Helper()
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		e.t.Fatal(err)
	}
	name := "test." + hex.EncodeToString(raw)
	e.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = e.b.DeleteQueue(ctx, name)
	})
	return name
}

func (e *env) publish(queue string, bodies ...[]byte) {
	e.t.Helper()
	for _, body := range bodies {
		if err := e.b.Publish(ctxFor(e.t), queue, body); err != nil {
			e.t.Fatalf("Publish: %v", err)
		}
	}
}

func (e *env) consume(queue string, prefetch int) delivery.Consumer {
	e.t.Helper()
	c, err := e.b.Consume(ctxFor(e.t), queue, prefetch)
	if err != nil {
		e.t.Fatalf("Consume: %v", err)
	}
	e.t.Cleanup(func() { _ = c.Close() })
	return c
}

func (e *env) next(c delivery.Consumer) delivery.Delivery {
	e.t.Helper()
	select {
	case d, ok := <-c.Deliveries():
		if !ok {
			e.t.Fatalf("выдачи закончились: %v", c.Err())
		}
		return d
	case <-time.After(waitLimit):
		e.t.Fatal("нет выдачи")
	}
	return delivery.Delivery{}
}

func (e *env) nothing(c delivery.Consumer) {
	e.t.Helper()
	select {
	case d, ok := <-c.Deliveries():
		if ok {
			e.t.Fatalf("лишняя выдача %d", d.ID)
		}
		e.t.Fatalf("выдачи закончились: %v", c.Err())
	case <-time.After(silence()):
	}
}

func numbered(n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = []byte(fmt.Sprintf("body-%03d", i))
	}
	return out
}

// Run проверяет реализацию брокера.
func Run(t *testing.T, factory Factory) {
	section := func(name string, maxMessages int, fn func(e *env)) {
		t.Run(name, func(t *testing.T) { fn(&env{t: t, b: factory(t, maxMessages)}) })
	}

	section("готовность", 0, func(e *env) {
		if err := e.b.Ready(); err != nil {
			e.t.Fatal(err)
		}
	})

	section("тело доходит байт в байт", 0, func(e *env) {
		q := e.queue()
		body := make([]byte, 53312)
		if _, err := rand.Read(body); err != nil {
			e.t.Fatal(err)
		}
		e.publish(q, body)
		c := e.consume(q, 1)
		d := e.next(c)
		if !bytes.Equal(d.Body, body) {
			e.t.Fatal("тело изменилось")
		}
		if d.Redelivered {
			e.t.Error("первая выдача отмечена как повторная")
		}
		if err := c.Ack(d.ID); err != nil {
			e.t.Fatal(err)
		}
	})

	section("публикация не создаёт очередь", 0, func(e *env) {
		q := e.name()
		err := e.b.Publish(ctxFor(e.t), q, []byte("x"))
		if !errors.Is(err, delivery.ErrQueueMissing) {
			e.t.Fatalf("Publish: %v", err)
		}
		if _, err := e.b.Consume(ctxFor(e.t), q, 1); !errors.Is(err, delivery.ErrQueueMissing) {
			e.t.Fatalf("очередь появилась: %v", err)
		}
	})

	section("объявление идемпотентно", 0, func(e *env) {
		q := e.queue()
		e.publish(q, []byte("x"))
		if err := e.b.EnsureQueue(ctxFor(e.t), q); err != nil {
			e.t.Fatal(err)
		}
		e.next(e.consume(q, 1))
	})

	section("заполненная очередь", 2, func(e *env) {
		q := e.queue()
		bodies := numbered(2)
		e.publish(q, bodies...)
		if err := e.b.Publish(ctxFor(e.t), q, []byte("лишнее")); !errors.Is(err, delivery.ErrQueueFull) {
			e.t.Fatalf("Publish: %v", err)
		}
		c := e.consume(q, 2)
		for _, want := range bodies {
			d := e.next(c)
			if !bytes.Equal(d.Body, want) {
				e.t.Fatalf("тело %q, ожидалось %q", d.Body, want)
			}
			if err := c.Ack(d.ID); err != nil {
				e.t.Fatal(err)
			}
		}
		e.nothing(c)
		// Очередь освободилась.
		e.publish(q, []byte("снова"))
	})

	section("не больше prefetch неподтверждённых", 0, func(e *env) {
		q := e.queue()
		e.publish(q, numbered(5)...)
		c := e.consume(q, 2)
		first := e.next(c)
		e.next(c)
		e.nothing(c)
		if err := c.Ack(first.ID); err != nil {
			e.t.Fatal(err)
		}
		e.next(c)
		e.nothing(c)
	})

	section("неподтверждённое возвращается", 0, func(e *env) {
		q := e.queue()
		e.publish(q, []byte("a"), []byte("b"))
		c := e.consume(q, 1)
		a := e.next(c)
		if err := c.Ack(a.ID); err != nil {
			e.t.Fatal(err)
		}
		b := e.next(c)
		if err := c.Close(); err != nil {
			e.t.Fatal(err)
		}
		if err := c.Close(); err != nil {
			e.t.Fatalf("повторное закрытие: %v", err)
		}
		again := e.consume(q, 1)
		d := e.next(again)
		if !bytes.Equal(d.Body, b.Body) || !d.Redelivered {
			e.t.Fatalf("повтор: %q, %v", d.Body, d.Redelivered)
		}
		if err := again.Ack(d.ID); err != nil {
			e.t.Fatal(err)
		}
		e.nothing(again)
	})

	section("порядок без повторов", 0, func(e *env) {
		q := e.queue()
		bodies := numbered(20)
		e.publish(q, bodies...)
		c := e.consume(q, 4)
		for _, want := range bodies {
			d := e.next(c)
			if !bytes.Equal(d.Body, want) {
				e.t.Fatalf("тело %q, ожидалось %q", d.Body, want)
			}
			if err := c.Ack(d.ID); err != nil {
				e.t.Fatal(err)
			}
		}
	})

	section("удаление очереди", 0, func(e *env) {
		q := e.queue()
		e.publish(q, []byte("x"))
		if err := e.b.DeleteQueue(ctxFor(e.t), q); err != nil {
			e.t.Fatal(err)
		}
		if err := e.b.DeleteQueue(ctxFor(e.t), q); err != nil {
			e.t.Fatalf("повторное удаление: %v", err)
		}
		if err := e.b.Publish(ctxFor(e.t), q, []byte("y")); !errors.Is(err, delivery.ErrQueueMissing) {
			e.t.Fatalf("Publish: %v", err)
		}
		if err := e.b.EnsureQueue(ctxFor(e.t), q); err != nil {
			e.t.Fatal(err)
		}
		// Содержимое удалено вместе с очередью.
		e.nothing(e.consume(q, 1))
	})

	section("удаление завершает потребителя", 0, func(e *env) {
		q := e.queue()
		c := e.consume(q, 1)
		if err := e.b.DeleteQueue(ctxFor(e.t), q); err != nil {
			e.t.Fatal(err)
		}
		select {
		case _, ok := <-c.Deliveries():
			if ok {
				e.t.Fatal("лишняя выдача")
			}
		case <-time.After(waitLimit):
			e.t.Fatal("потребитель не завершился")
		}
		if !errors.Is(c.Err(), delivery.ErrConsumerCancelled) {
			e.t.Fatalf("причина: %v", c.Err())
		}
	})

	section("потребление без очереди", 0, func(e *env) {
		if _, err := e.b.Consume(ctxFor(e.t), e.name(), 1); !errors.Is(err, delivery.ErrQueueMissing) {
			e.t.Fatalf("Consume: %v", err)
		}
	})

	section("отмена публикации", 0, func(e *env) {
		q := e.queue()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := e.b.Publish(ctx, q, []byte("x")); !errors.Is(err, context.Canceled) {
			e.t.Fatalf("Publish: %v", err)
		}
		// После отмены брокер работает.
		e.publish(q, []byte("y"))
		d := e.next(e.consume(q, 1))
		if string(d.Body) != "y" {
			e.t.Fatalf("тело %q", d.Body)
		}
	})
}
