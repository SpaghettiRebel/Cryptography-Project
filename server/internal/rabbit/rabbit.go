// Пакет rabbit реализует delivery.Broker на RabbitMQ и сам восстанавливает соединение. О чатах пакет не
// знает. Адрес брокера и тексты его ошибок в журнал и наружу не попадают: адрес содержит пароль.
package rabbit

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"cryptochat/server/internal/delivery"
)

const (
	defaultMaxLengthBytes = 64 << 20
	defaultMaxLength      = 4096
	publisherChannels     = 4
	dialTimeout           = 5 * time.Second
	heartbeat             = 5 * time.Second
	minPause              = time.Second
	maxPause              = 5 * time.Second
	closeTimeout          = 2 * time.Second
)

// Limits задаёт пределы очереди; нулевое поле означает значение по умолчанию. Меньшие значения передают
// только тесты: брокер отвергает повторное объявление очереди с другими аргументами.
type Limits struct {
	MaxLengthBytes int64
	MaxLength      int64
}

// Broker реализует delivery.Broker.
type Broker struct {
	url    string
	logger *slog.Logger
	args   amqp.Table
	pubs   [publisherChannels]*publisher
	turn   atomic.Uint64

	mu   sync.RWMutex
	conn *amqp.Connection
}

// New создаёт брокер; соединение устанавливает Run.
func New(url string, logger *slog.Logger, limits Limits) *Broker {
	if logger == nil {
		logger = slog.Default()
	}
	if limits.MaxLengthBytes == 0 {
		limits.MaxLengthBytes = defaultMaxLengthBytes
	}
	if limits.MaxLength == 0 {
		limits.MaxLength = defaultMaxLength
	}
	b := &Broker{url: url, logger: logger, args: amqp.Table{
		"x-max-length-bytes": limits.MaxLengthBytes,
		"x-max-length":       limits.MaxLength,
		"x-overflow":         "reject-publish",
	}}
	for i := range b.pubs {
		b.pubs[i] = &publisher{sem: make(chan struct{}, 1)}
	}
	return b
}

// Run держит соединение, пока ctx не отменён. Первая попытка идёт сразу, затем пауза от 1 до 5 с.
func (b *Broker) Run(ctx context.Context) {
	pause := minPause
	wasConnected := true
	for ctx.Err() == nil {
		conn, err := amqp.DialConfig(b.url, amqp.Config{Dial: amqp.DefaultDial(dialTimeout), Heartbeat: heartbeat})
		if err != nil {
			// Текст ошибки не пишется: он может содержать адрес.
			if wasConnected {
				b.logger.Warn("брокер недоступен")
				wasConnected = false
			}
			select {
			case <-ctx.Done():
			case <-time.After(pause):
			}
			pause = min(pause*2, maxPause)
			continue
		}
		pause = minPause
		wasConnected = true
		closed := conn.NotifyClose(make(chan *amqp.Error, 1))
		b.setConn(conn)
		b.logger.Info("соединение с брокером установлено")
		select {
		case <-ctx.Done():
		case <-closed:
			b.logger.Warn("соединение с брокером потеряно")
		}
		b.setConn(nil)
		_ = conn.Close()
	}
}

func (b *Broker) setConn(conn *amqp.Connection) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.conn = conn
}

func (b *Broker) connection() (*amqp.Connection, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.conn == nil || b.conn.IsClosed() {
		return nil, delivery.ErrBrokerUnavailable
	}
	return b.conn, nil
}

// Ready реализует delivery.Broker: готовность равна открытому соединению.
func (b *Broker) Ready() error {
	_, err := b.connection()
	return err
}

// Close закрывает соединение. Run при этом завершается только по своему контексту.
func (b *Broker) Close() {
	b.mu.Lock()
	conn := b.conn
	b.conn = nil
	b.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// inChannel выполняет служебную операцию на отдельном канале. Библиотека не учитывает ctx, поэтому
// операция идёт в своей горутине: её завершит ответ брокера или закрытие соединения.
func (b *Broker) inChannel(ctx context.Context, fn func(*amqp.Channel) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, err := b.connection()
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		ch, err := conn.Channel()
		if err != nil {
			done <- delivery.ErrBrokerUnavailable
			return
		}
		defer func() { _ = ch.Close() }()
		done <- fn(ch)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// EnsureQueue реализует delivery.Broker: долговечная классическая очередь с постоянными аргументами.
func (b *Broker) EnsureQueue(ctx context.Context, queue string) error {
	return b.inChannel(ctx, func(ch *amqp.Channel) error {
		if _, err := ch.QueueDeclare(queue, true, false, false, false, b.args); err != nil {
			return delivery.ErrBrokerUnavailable
		}
		return nil
	})
}

// DeleteQueue реализует delivery.Broker. Удаление отсутствующей очереди брокер не считает ошибкой.
func (b *Broker) DeleteQueue(ctx context.Context, queue string) error {
	return b.inChannel(ctx, func(ch *amqp.Channel) error {
		if _, err := ch.QueueDelete(queue, false, false, false); err != nil {
			return delivery.ErrBrokerUnavailable
		}
		return nil
	})
}

// Publish реализует delivery.Broker.
func (b *Broker) Publish(ctx context.Context, queue string, body []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, err := b.connection()
	if err != nil {
		return err
	}
	return b.pubs[b.turn.Add(1)%publisherChannels].publish(ctx, conn, queue, body)
}

// publisher — канал издателя с подтверждениями. Блокировка на всю публикацию гарантирует, что после
// подтверждения можно без гонки прочитать возврат.
type publisher struct {
	sem     chan struct{}
	ch      *amqp.Channel
	returns chan amqp.Return
}

func (p *publisher) publish(ctx context.Context, conn *amqp.Connection, queue string, body []byte) error {
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	// Запись в сокет не учитывает ctx и может застрять при блокировке соединения брокером, поэтому она идёт
	// в своей горутине. Канал освобождается, когда запись закончится или соединение закроется.
	done := make(chan error, 1)
	go func() {
		defer func() { <-p.sem }()
		done <- p.publishLocked(ctx, conn, queue, body)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *publisher) open(conn *amqp.Connection) error {
	if p.ch != nil && !p.ch.IsClosed() {
		return nil
	}
	ch, err := conn.Channel()
	if err != nil {
		return delivery.ErrBrokerUnavailable
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return delivery.ErrBrokerUnavailable
	}
	p.ch = ch
	p.returns = ch.NotifyReturn(make(chan amqp.Return, 1))
	return nil
}

// drop закрывает канал: ответ на прерванную публикацию не должен достаться следующей.
func (p *publisher) drop() {
	ch := p.ch
	p.ch = nil
	if ch != nil {
		go func() { _ = ch.Close() }()
	}
}

func (p *publisher) publishLocked(ctx context.Context, conn *amqp.Connection, queue string, body []byte) error {
	if err := p.open(conn); err != nil {
		return err
	}
	confirm, err := p.ch.PublishWithDeferredConfirmWithContext(ctx, "", queue, true, false, amqp.Publishing{
		ContentType:  "application/octet-stream",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	})
	if err != nil {
		p.drop()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return delivery.ErrBrokerUnavailable
	}
	acked, err := confirm.WaitContext(ctx)
	if err != nil {
		p.drop()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return delivery.ErrBrokerUnavailable
	}
	if !acked {
		// При закрытии канала библиотека тоже отвечает отказом.
		if p.ch.IsClosed() {
			p.drop()
			return delivery.ErrBrokerUnavailable
		}
		return delivery.ErrQueueFull
	}
	select {
	case _, ok := <-p.returns:
		if ok {
			return delivery.ErrQueueMissing
		}
		p.drop()
		return delivery.ErrBrokerUnavailable
	default:
		return nil
	}
}
