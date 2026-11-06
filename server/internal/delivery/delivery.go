// Пакет delivery принимает фрагменты шифротекста и передаёт их собеседнику через брокер. Для сервиса
// фрагмент — непрозрачные байты: он их не разбирает и нигде не хранит. Пакет не знает о gRPC, Postgres и
// RabbitMQ и работает через интерфейсы store.DeliveryStore и Broker.
package delivery

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"time"

	"cryptochat/server/internal/chats"
	"cryptochat/server/internal/store"
)

const (
	// MaxFragmentSize — предел сервера на один фрагмент. Вместе с конвертом он меньше предела запроса.
	MaxFragmentSize = 60 << 10
	// Prefetch — наибольшее число неподтверждённых фрагментов у одного потока.
	Prefetch = 16
	// CleanupBatch — наибольшее число чатов за один проход удаления очередей.
	CleanupBatch = 100
	// PublishTimeout — общий предел всей работы с брокером при отправке одного фрагмента.
	PublishTimeout = 5 * time.Second

	defaultAckTimeout      = 60 * time.Second
	defaultCheckInterval   = 2 * time.Second
	defaultCleanupInterval = time.Second
)

var (
	ErrInvalidFragment      = errors.New("delivery: пустой фрагмент")
	ErrFragmentTooLarge     = errors.New("delivery: фрагмент больше допустимого")
	ErrChatNotReady         = errors.New("delivery: чат не готов к обмену")
	ErrStreamReplaced       = errors.New("delivery: поток заменён новым")
	ErrAckTimeout           = errors.New("delivery: нет подтверждения")
	ErrInvalidStreamRequest = errors.New("delivery: неверная последовательность сообщений")
	ErrStopping             = errors.New("delivery: сервер останавливается")
)

// Options задаёт сроки; нулевое поле означает значение по умолчанию. Рабочий сервер передаёт пустую
// структуру, тесты других пакетов — короткие сроки.
type Options struct {
	AckTimeout      time.Duration
	CheckInterval   time.Duration
	CleanupInterval time.Duration
}

func (o Options) withDefaults() Options {
	if o.AckTimeout == 0 {
		o.AckTimeout = defaultAckTimeout
	}
	if o.CheckInterval == 0 {
		o.CheckInterval = defaultCheckInterval
	}
	if o.CleanupInterval == 0 {
		o.CleanupInterval = defaultCleanupInterval
	}
	return o
}

// Service — сервис доставки.
type Service struct {
	store  store.DeliveryStore
	chats  store.ChatStore
	broker Broker
	logger *slog.Logger
	opt    Options

	mu       sync.Mutex
	known    map[string]struct{} // объявленные очереди
	orphans  map[string]struct{} // очереди закрытых чатов, которые не удалось удалить сразу
	streams  map[streamKey]*Subscription
	locks    map[streamKey]*pairLock
	stopping bool
}

// New создаёт сервис.
func New(s store.DeliveryStore, chatStore store.ChatStore, b Broker, logger *slog.Logger, opt Options) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store: s, chats: chatStore, broker: b, logger: logger, opt: opt.withDefaults(),
		known:   map[string]struct{}{},
		orphans: map[string]struct{}{},
		streams: map[streamKey]*Subscription{},
		locks:   map[streamKey]*pairLock{},
	}
}

// QueueName возвращает имя очереди чата и получателя.
func QueueName(id [16]byte, userID string) string {
	return "chat." + hex.EncodeToString(id[:]) + "." + userID
}

// Send проверяет право на отправку и публикует фрагмент в очередь собеседника. Успех означает, что брокер
// принял фрагмент; получателя сервис не ждёт.
func (s *Service) Send(ctx context.Context, userID string, rawChatID, fragment []byte) error {
	// Длина проверяется первой: её результат не зависит от чата и не раскрывает его существование.
	if len(fragment) == 0 {
		return ErrInvalidFragment
	}
	if len(fragment) > MaxFragmentSize {
		return ErrFragmentTooLarge
	}
	id, err := chats.ParseID(rawChatID)
	if err != nil {
		return err
	}
	st, err := s.store.DeliveryState(ctx, id, userID)
	if err != nil {
		return err
	}
	switch {
	case st.Closed:
		return store.ErrChatClosed
	case !st.Connected:
		return store.ErrNotConnected
	case !st.Ready:
		return ErrChatNotReady
	}
	// Один предел на все шаги брокера, иначе объявление и две публикации заняли бы втрое больше.
	brokerCtx, cancel := context.WithTimeout(ctx, PublishTimeout)
	defer cancel()
	return brokerError(ctx, s.publish(brokerCtx, id, QueueName(id, st.PeerID), userID, fragment))
}

// brokerError различает отмену запроса клиентом и истечение предела работы с брокером.
func brokerError(parent context.Context, err error) error {
	if err == nil {
		return nil
	}
	if perr := parent.Err(); perr != nil {
		return perr
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ErrBrokerUnavailable
	}
	return err
}

// publish объявляет очередь при необходимости и публикует фрагмент; потерянную очередь активного чата
// восстанавливает одной повторной публикацией.
func (s *Service) publish(ctx context.Context, id [16]byte, queue, userID string, fragment []byte) error {
	for attempt := 0; ; attempt++ {
		if !s.isKnown(queue) {
			if err := s.ensureQueue(ctx, id, queue, userID); err != nil {
				return err
			}
		}
		err := s.broker.Publish(ctx, queue, fragment)
		if !errors.Is(err, ErrQueueMissing) {
			return err
		}
		s.forget(queue)
		st, err := s.store.DeliveryState(ctx, id, userID)
		if err != nil {
			return err
		}
		if st.Closed {
			return store.ErrChatClosed
		}
		if attempt > 0 {
			return ErrBrokerUnavailable
		}
	}
}

// ensureQueue объявляет очередь и снова читает состояние: публикация очередь не создаёт, а чат мог закрыться
// за это время. Закрытие после этой проверки обработчик увидит уже после объявления и удалит очередь сам.
func (s *Service) ensureQueue(ctx context.Context, id [16]byte, queue, userID string) error {
	if err := s.broker.EnsureQueue(ctx, queue); err != nil {
		return err
	}
	st, err := s.store.DeliveryState(ctx, id, userID)
	if err != nil {
		return err
	}
	if st.Closed {
		s.deleteLate(ctx, id, queue)
		return store.ErrChatClosed
	}
	s.remember(queue)
	return nil
}

// deleteLate удаляет очередь, объявленную в гонке с закрытием. Обработчик мог уже отметить этот чат, поэтому
// при сбое отметка снимается: так очередь переживёт и перезапуск сервера. Если хранилище тоже не ответило,
// остаётся память процесса; в ней очередь лежит и при успехе, чтобы удалить её следующим же проходом.
func (s *Service) deleteLate(ctx context.Context, id [16]byte, queue string) {
	s.forget(queue)
	if err := s.broker.DeleteQueue(ctx, queue); err == nil {
		return
	}
	s.mu.Lock()
	s.orphans[queue] = struct{}{}
	s.mu.Unlock()
	// Контекст вызова мог уже истечь вместе с попыткой удаления.
	storeCtx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	if err := s.store.ReopenQueueCleanup(storeCtx, id); err != nil {
		s.logger.Warn("отметка об удалении очередей не снята: хранилище не ответило")
	}
}

func (s *Service) isKnown(queue string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.known[queue]
	return ok
}

func (s *Service) remember(queue string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.known[queue] = struct{}{}
}

func (s *Service) forget(queue string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.known, queue)
}
