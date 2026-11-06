// Пакет membroker — брокер в памяти для тестов. В сервер не входит.
package membroker

import (
	"context"
	"errors"
	"sync"

	"cryptochat/server/internal/delivery"
)

const defaultMaxMessages = 4096

type message struct {
	body        []byte
	redelivered bool
}

type queue struct {
	ready     []message
	consumers []*Consumer
	next      int // следующий потребитель по кругу
}

// Broker реализует delivery.Broker.
type Broker struct {
	mu          sync.Mutex
	queues      map[string]*queue
	down        bool
	maxMessages int
}

// New создаёт брокер. maxMessages больше нуля задаёт предел длины очереди.
func New(maxMessages int) *Broker {
	if maxMessages <= 0 {
		maxMessages = defaultMaxMessages
	}
	return &Broker{queues: map[string]*queue{}, maxMessages: maxMessages}
}

// SetDown имитирует сбой: вызовы дают ErrBrokerUnavailable, потребители завершаются, а принятые сообщения
// сохраняются, как у настоящего брокера с постоянными сообщениями.
func (b *Broker) SetDown(down bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.down = down
	if down {
		for _, q := range b.queues {
			for _, c := range append([]*Consumer(nil), q.consumers...) {
				c.endLocked(delivery.ErrBrokerUnavailable)
			}
		}
	}
}

// Ready реализует delivery.Broker.
func (b *Broker) Ready() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.down {
		return delivery.ErrBrokerUnavailable
	}
	return nil
}

func (b *Broker) enter(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.down {
		b.mu.Unlock()
		return nil, delivery.ErrBrokerUnavailable
	}
	return b.mu.Unlock, nil
}

// EnsureQueue реализует delivery.Broker.
func (b *Broker) EnsureQueue(ctx context.Context, name string) error {
	leave, err := b.enter(ctx)
	if err != nil {
		return err
	}
	defer leave()
	if _, ok := b.queues[name]; !ok {
		b.queues[name] = &queue{}
	}
	return nil
}

// Publish реализует delivery.Broker.
func (b *Broker) Publish(ctx context.Context, name string, body []byte) error {
	leave, err := b.enter(ctx)
	if err != nil {
		return err
	}
	defer leave()
	q, ok := b.queues[name]
	if !ok {
		return delivery.ErrQueueMissing
	}
	if len(q.ready) >= b.maxMessages {
		return delivery.ErrQueueFull
	}
	q.ready = append(q.ready, message{body: append([]byte(nil), body...)})
	q.dispatch()
	return nil
}

// Consume реализует delivery.Broker.
func (b *Broker) Consume(ctx context.Context, name string, prefetch int) (delivery.Consumer, error) {
	leave, err := b.enter(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	q, ok := b.queues[name]
	if !ok {
		return nil, delivery.ErrQueueMissing
	}
	c := &Consumer{broker: b, queue: q, prefetch: prefetch, out: make(chan delivery.Delivery, prefetch), unacked: map[uint64]message{}}
	q.consumers = append(q.consumers, c)
	q.dispatch()
	return c, nil
}

// DeleteQueue реализует delivery.Broker.
func (b *Broker) DeleteQueue(ctx context.Context, name string) error {
	leave, err := b.enter(ctx)
	if err != nil {
		return err
	}
	defer leave()
	q, ok := b.queues[name]
	if !ok {
		return nil
	}
	for _, c := range append([]*Consumer(nil), q.consumers...) {
		c.endLocked(delivery.ErrConsumerCancelled)
	}
	delete(b.queues, name)
	return nil
}

// QueueLen возвращает число готовых и неподтверждённых сообщений очереди и признак её существования.
func (b *Broker) QueueLen(name string) (int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	q, ok := b.queues[name]
	if !ok {
		return 0, false
	}
	n := len(q.ready)
	for _, c := range q.consumers {
		n += len(c.unacked)
	}
	return n, true
}

// Queues возвращает имена всех очередей.
func (b *Broker) Queues() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.queues))
	for name := range b.queues {
		out = append(out, name)
	}
	return out
}

// Bodies возвращает копии тел ожидающих сообщений очереди.
func (b *Broker) Bodies(name string) [][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	q, ok := b.queues[name]
	if !ok {
		return nil
	}
	out := make([][]byte, 0, len(q.ready))
	for _, m := range q.ready {
		out = append(out, append([]byte(nil), m.body...))
	}
	return out
}

// dispatch раздаёт готовые сообщения по кругу в пределах prefetch. Вызывается под блокировкой; запись в
// канал не блокируется, поскольку его ёмкость равна prefetch.
func (q *queue) dispatch() {
	for len(q.ready) > 0 {
		c := q.pick()
		if c == nil {
			return
		}
		m := q.ready[0]
		q.ready = q.ready[1:]
		c.seq++
		c.unacked[c.seq] = m
		c.out <- delivery.Delivery{ID: c.seq, Body: append([]byte(nil), m.body...), Redelivered: m.redelivered}
	}
}

func (q *queue) pick() *Consumer {
	for i := range q.consumers {
		c := q.consumers[(q.next+i)%len(q.consumers)]
		if len(c.unacked) < c.prefetch {
			q.next = (q.next + i + 1) % len(q.consumers)
			return c
		}
	}
	return nil
}

// Consumer реализует delivery.Consumer.
type Consumer struct {
	broker   *Broker
	queue    *queue
	prefetch int
	out      chan delivery.Delivery
	seq      uint64
	unacked  map[uint64]message
	ended    bool
	err      error
}

// Deliveries реализует delivery.Consumer.
func (c *Consumer) Deliveries() <-chan delivery.Delivery { return c.out }

// Err реализует delivery.Consumer.
func (c *Consumer) Err() error {
	c.broker.mu.Lock()
	defer c.broker.mu.Unlock()
	return c.err
}

// Ack реализует delivery.Consumer.
func (c *Consumer) Ack(id uint64) error {
	c.broker.mu.Lock()
	defer c.broker.mu.Unlock()
	if c.ended {
		return delivery.ErrBrokerUnavailable
	}
	if _, ok := c.unacked[id]; !ok {
		return errors.New("membroker: неизвестный номер")
	}
	delete(c.unacked, id)
	c.queue.dispatch()
	return nil
}

// Close реализует delivery.Consumer.
func (c *Consumer) Close() error {
	c.broker.mu.Lock()
	defer c.broker.mu.Unlock()
	c.endLocked(nil)
	return nil
}

// endLocked завершает потребителя и возвращает неподтверждённые сообщения в начало очереди.
func (c *Consumer) endLocked(cause error) {
	if c.ended {
		return
	}
	c.ended = true
	c.err = cause
	q := c.queue
	for i, other := range q.consumers {
		if other == c {
			q.consumers = append(q.consumers[:i], q.consumers[i+1:]...)
			break
		}
	}
	q.next = 0
	returned := make([]message, 0, len(c.unacked)+len(q.ready))
	ids := make([]uint64, 0, len(c.unacked))
	for id := range c.unacked {
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	for _, id := range ids {
		m := c.unacked[id]
		m.redelivered = true
		returned = append(returned, m)
	}
	q.ready = append(returned, q.ready...)
	c.unacked = map[uint64]message{}
	for len(c.out) > 0 {
		<-c.out
	}
	close(c.out)
	q.dispatch()
}
