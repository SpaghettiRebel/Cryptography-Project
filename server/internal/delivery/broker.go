package delivery

import (
	"context"
	"errors"
)

var (
	// ErrBrokerUnavailable — нет соединения или истекло время.
	ErrBrokerUnavailable = errors.New("delivery: брокер недоступен")
	// ErrQueueMissing — публикация вернулась: очереди нет.
	ErrQueueMissing = errors.New("delivery: очереди нет")
	// ErrQueueFull — брокер отверг публикацию.
	ErrQueueFull = errors.New("delivery: очередь заполнена")
	// ErrConsumerCancelled — очередь удалена при живом потребителе.
	ErrConsumerCancelled = errors.New("delivery: потребитель отменён")
)

// Delivery — одна выдача фрагмента. Номер действует только у своего потребителя.
type Delivery struct {
	ID          uint64
	Body        []byte
	Redelivered bool
}

// Consumer — потребитель одной очереди с ручным подтверждением.
type Consumer interface {
	// Deliveries закрывается при завершении; причину даёт Err.
	Deliveries() <-chan Delivery
	Err() error
	Ack(id uint64) error
	// Close закрывает потребление; неподтверждённые возвращаются в очередь. Идемпотентна.
	Close() error
}

// Broker — всё, что сервису нужно от брокера сообщений.
type Broker interface {
	Ready() error
	EnsureQueue(ctx context.Context, queue string) error
	// Publish возвращается после подтверждения брокера. Очередь не создаёт. Отмена и истечение ctx дают
	// ошибку ctx; сервис сам решает, что означает истечение его предела.
	Publish(ctx context.Context, queue string, body []byte) error
	// Consume: отсутствующая очередь даёт ErrQueueMissing.
	Consume(ctx context.Context, queue string, prefetch int) (Consumer, error)
	// DeleteQueue идемпотентна: отсутствующая очередь не ошибка.
	DeleteQueue(ctx context.Context, queue string) error
}
