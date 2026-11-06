package rabbit

import (
	"context"
	"errors"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"cryptochat/server/internal/delivery"
)

// Consume реализует delivery.Broker: у потребителя свой канал с ограничением и ручным подтверждением.
func (b *Broker) Consume(ctx context.Context, queue string, prefetch int) (delivery.Consumer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := b.connection()
	if err != nil {
		return nil, err
	}
	type result struct {
		c   *consumer
		err error
	}
	done := make(chan result, 1)
	go func() {
		c, err := newConsumer(conn, queue, prefetch)
		done <- result{c, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return nil, r.err
		}
		return r.c, nil
	case <-ctx.Done():
		// Потребитель, созданный после отмены, никому не нужен.
		go func() {
			if r := <-done; r.c != nil {
				_ = r.c.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

func newConsumer(conn *amqp.Connection, queue string, prefetch int) (*consumer, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, delivery.ErrBrokerUnavailable
	}
	if err := ch.Qos(prefetch, 0, false); err != nil {
		_ = ch.Close()
		return nil, delivery.ErrBrokerUnavailable
	}
	cancelled := ch.NotifyCancel(make(chan string, 1))
	closed := ch.NotifyClose(make(chan *amqp.Error, 1))
	in, err := ch.Consume(queue, "", false, false, false, false, nil)
	if err != nil {
		_ = ch.Close()
		var amqpErr *amqp.Error
		if errors.As(err, &amqpErr) && amqpErr.Code == amqp.NotFound {
			return nil, delivery.ErrQueueMissing
		}
		return nil, delivery.ErrBrokerUnavailable
	}
	c := &consumer{ch: ch, out: make(chan delivery.Delivery), stop: make(chan struct{}), stopped: make(chan struct{})}
	go c.pump(in, cancelled, closed)
	return c, nil
}

type consumer struct {
	ch      *amqp.Channel
	out     chan delivery.Delivery
	stop    chan struct{}
	stopped chan struct{}
	once    sync.Once

	mu  sync.Mutex
	err error
}

// pump переносит выдачи библиотеки в канал потребителя и определяет причину завершения.
func (c *consumer) pump(in <-chan amqp.Delivery, cancelled <-chan string, closed <-chan *amqp.Error) {
	defer close(c.stopped)
	defer close(c.out)
	for {
		select {
		case <-c.stop:
			return
		case _, ok := <-cancelled:
			if ok {
				c.setErr(delivery.ErrConsumerCancelled)
			} else {
				c.setErr(delivery.ErrBrokerUnavailable)
			}
			return
		case <-closed:
			c.setErr(delivery.ErrBrokerUnavailable)
			return
		case d, ok := <-in:
			if !ok {
				// Библиотека сообщает об отмене до закрытия канала выдач.
				select {
				case _, isCancel := <-cancelled:
					if isCancel {
						c.setErr(delivery.ErrConsumerCancelled)
						return
					}
				default:
				}
				c.setErr(delivery.ErrBrokerUnavailable)
				return
			}
			select {
			case c.out <- delivery.Delivery{ID: d.DeliveryTag, Body: d.Body, Redelivered: d.Redelivered}:
			case <-c.stop:
				return
			case _, ok := <-cancelled:
				if ok {
					c.setErr(delivery.ErrConsumerCancelled)
				} else {
					c.setErr(delivery.ErrBrokerUnavailable)
				}
				return
			case <-closed:
				c.setErr(delivery.ErrBrokerUnavailable)
				return
			}
		}
	}
}

func (c *consumer) setErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		c.err = err
	}
}

// Deliveries реализует delivery.Consumer.
func (c *consumer) Deliveries() <-chan delivery.Delivery { return c.out }

// Err реализует delivery.Consumer.
func (c *consumer) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Ack реализует delivery.Consumer.
func (c *consumer) Ack(id uint64) error {
	if err := c.ch.Ack(id, false); err != nil {
		return delivery.ErrBrokerUnavailable
	}
	return nil
}

// Close реализует delivery.Consumer: закрытие канала возвращает неподтверждённые в очередь. При зависшем
// соединении ожидание ограничено: канал закроется вместе с ним.
func (c *consumer) Close() error {
	c.once.Do(func() {
		close(c.stop)
		<-c.stopped
		done := make(chan struct{})
		go func() {
			_ = c.ch.Close()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(closeTimeout):
		}
	})
	return nil
}
