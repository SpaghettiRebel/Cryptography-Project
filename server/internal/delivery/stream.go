package delivery

import (
	"context"
	"errors"
	"sync"
	"time"

	"cryptochat/server/internal/chats"
	"cryptochat/server/internal/store"
)

// storeTimeout ограничивает сверку потока и запись снятия подключения.
const storeTimeout = 5 * time.Second

type streamKey struct {
	id     [16]byte
	userID string
}

// pairLock — блокировка пары «чат и участник»; запись удаляется, когда её никто не держит.
type pairLock struct {
	mu   sync.Mutex
	refs int
}

func (s *Service) lockPair(key streamKey) func() {
	s.mu.Lock()
	l := s.locks[key]
	if l == nil {
		l = &pairLock{}
		s.locks[key] = l
	}
	l.refs++
	s.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		s.mu.Lock()
		defer s.mu.Unlock()
		l.refs--
		if l.refs == 0 {
			delete(s.locks, key)
		}
	}
}

// Open начинает потребление, подключает участника и заменяет его прежний поток. Подключение и замена идут
// последними, поэтому неудачное открытие не меняет ни признак, ни старый поток. Всё это идёт под блокировкой
// пары: завершение старого потока не снимет подключение, установленное новым. Брокер коротко видит двух
// потребителей, а неподтверждённое старым вернётся в очередь при его закрытии. checkSession вызывается при
// каждой сверке: сервис не знает о токенах, а проверку сессии этого потока передаёт вызывающий.
func (s *Service) Open(ctx context.Context, userID string, rawChatID []byte, checkSession func(context.Context) error) (*Subscription, error) {
	id, err := chats.ParseID(rawChatID)
	if err != nil {
		return nil, err
	}
	key := streamKey{id: id, userID: userID}
	unlock := s.lockPair(key)
	defer unlock()

	s.mu.Lock()
	stopping, old := s.stopping, s.streams[key]
	s.mu.Unlock()
	if stopping {
		return nil, ErrStopping
	}
	// Очередь объявляется только для участника активного чата.
	st, err := s.store.DeliveryState(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	if st.Closed {
		return nil, store.ErrChatClosed
	}

	brokerCtx, cancel := context.WithTimeout(ctx, PublishTimeout)
	defer cancel()
	queue := QueueName(id, userID)
	if err := s.ensureQueue(brokerCtx, id, queue, userID); err != nil {
		return nil, brokerError(ctx, err)
	}
	consumer, err := s.broker.Consume(brokerCtx, queue, Prefetch)
	if errors.Is(err, ErrQueueMissing) {
		// Очередь только что объявлена и состояние прочитано: её мог удалить только обработчик закрытия.
		return nil, store.ErrChatClosed
	}
	if err != nil {
		return nil, brokerError(ctx, err)
	}
	if _, err := s.chats.SetConnected(ctx, id, userID, true); err != nil {
		_ = consumer.Close()
		return nil, err
	}
	if old != nil {
		old.terminate(ErrStreamReplaced, true)
	}

	subCtx, subCancel := context.WithCancel(context.Background())
	sub := &Subscription{
		svc: s, key: key, consumer: consumer, checkSession: checkSession,
		ctx: subCtx, cancel: subCancel,
		out: make(chan Delivery), done: make(chan struct{}), unacked: map[uint64]*pendingAck{},
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		subCancel()
		_ = consumer.Close()
		return nil, ErrStopping
	}
	s.streams[key] = sub
	s.mu.Unlock()
	go sub.run()
	return sub, nil
}

// Shutdown завершает все потоки причиной ErrStopping. Подключения участников остаются.
func (s *Service) Shutdown() {
	s.mu.Lock()
	s.stopping = true
	subs := make([]*Subscription, 0, len(s.streams))
	for _, sub := range s.streams {
		subs = append(subs, sub)
	}
	s.mu.Unlock()
	for _, sub := range subs {
		sub.terminate(ErrStopping, false)
	}
}

// pendingAck — неподтверждённая доставка. Тела фрагмента здесь нет. Срок считается от выдачи клиенту: фрагмент,
// ждущий за другими, времени клиента не расходует.
type pendingAck struct {
	since  time.Time
	issued bool
}

// Subscription — открытый поток получения.
type Subscription struct {
	svc          *Service
	key          streamKey
	consumer     Consumer
	checkSession func(context.Context) error
	ctx          context.Context
	cancel       context.CancelFunc
	out          chan Delivery
	done         chan struct{}

	mu       sync.Mutex
	unacked  map[uint64]*pendingAck
	finished bool
	err      error
}

// Deliveries выдаёт фрагменты и закрывается по завершении потока.
func (sub *Subscription) Deliveries() <-chan Delivery { return sub.out }

// Done закрывается при завершении потока.
func (sub *Subscription) Done() <-chan struct{} { return sub.done }

// Err возвращает причину завершения.
func (sub *Subscription) Err() error {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	return sub.err
}

// Close завершает поток с причиной, которую определил вызывающий, и применяет правила снятия подключения.
// Повторный вызов ничего не делает.
func (sub *Subscription) Close(cause error) { sub.terminate(cause, false) }

// Ack подтверждает фрагмент брокеру. Подтверждение неизвестного номера закрыло бы канал брокера, поэтому
// неизвестный, повторный и ещё не выданный номера игнорируются.
func (sub *Subscription) Ack(id uint64) {
	sub.mu.Lock()
	p, ok := sub.unacked[id]
	if !ok || !p.issued || sub.finished {
		sub.mu.Unlock()
		return
	}
	delete(sub.unacked, id)
	sub.mu.Unlock()
	if err := sub.consumer.Ack(id); err != nil {
		sub.terminate(ErrBrokerUnavailable, false)
	}
}

// keepsConnection перечисляет причины, после которых подключение не снимается: либо клиент вернётся сам,
// либо признак уже снят другой операцией.
func keepsConnection(cause error) bool {
	for _, target := range []error{ErrStreamReplaced, ErrBrokerUnavailable, store.ErrUnavailable, ErrStopping, store.ErrNotConnected, store.ErrChatClosed} {
		if errors.Is(cause, target) {
			return true
		}
	}
	return false
}

// terminate завершает поток один раз. Первый вызов не держит блокировок на время работы: обработчик потока
// может ждать блокировку пары, которую держит Open, а Open вызывает здесь же terminate старого потока.
func (sub *Subscription) terminate(cause error, havePairLock bool) {
	sub.mu.Lock()
	if sub.finished {
		sub.mu.Unlock()
		return
	}
	sub.finished = true
	sub.err = cause
	sub.mu.Unlock()
	sub.cancel()
	close(sub.done)
	// Закрытие возвращает неподтверждённые фрагменты в очередь.
	_ = sub.consumer.Close()

	s := sub.svc
	if !havePairLock {
		unlock := s.lockPair(sub.key)
		defer unlock()
	}
	s.mu.Lock()
	current := s.streams[sub.key] == sub
	if current {
		delete(s.streams, sub.key)
	}
	s.mu.Unlock()
	if !current || keepsConnection(cause) {
		return
	}
	// Контекст потока к этому моменту отменён, поэтому у записи свой.
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	if _, err := s.chats.SetConnected(ctx, sub.key.id, sub.key.userID, false); err != nil {
		s.logger.Error("подключение не снято: хранилище не ответило")
	}
}

// run переносит выдачи брокера в поток и сверяет его состояние. Запись в канал не держит сверку: клиент,
// который не читает, не мешает завершению.
func (sub *Subscription) run() {
	defer close(sub.out)
	opt := sub.svc.opt
	check := time.NewTicker(opt.CheckInterval)
	defer check.Stop()
	ackTick := time.NewTicker(max(min(opt.AckTimeout/8, time.Second), time.Millisecond))
	defer ackTick.Stop()

	in := sub.consumer.Deliveries()
	var queue []Delivery
	for {
		var send chan Delivery
		var head Delivery
		if len(queue) > 0 {
			head = queue[0]
			send = sub.out
			sub.markIssued(head.ID)
		}
		select {
		case <-sub.done:
			return
		case d, ok := <-in:
			if !ok {
				sub.consumerEnded()
				return
			}
			sub.track(d.ID)
			queue = append(queue, d)
		case send <- head:
			queue[0] = Delivery{}
			queue = queue[1:]
		case <-ackTick.C:
			if sub.ackExpired(opt.AckTimeout) {
				sub.terminate(ErrAckTimeout, false)
				return
			}
		case <-check.C:
			if err := sub.verify(); err != nil {
				sub.terminate(err, false)
				return
			}
		}
	}
}

func (sub *Subscription) track(id uint64) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	sub.unacked[id] = &pendingAck{}
}

func (sub *Subscription) markIssued(id uint64) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if p, ok := sub.unacked[id]; ok && !p.issued {
		p.issued = true
		p.since = time.Now()
	}
}

// ackExpired смотрит на самый старый неподтверждённый номер.
func (sub *Subscription) ackExpired(limit time.Duration) bool {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	now := time.Now()
	for _, p := range sub.unacked {
		if p.issued && now.Sub(p.since) >= limit {
			return true
		}
	}
	return false
}

// verify сверяет сессию и состояние чата; так обнаруживаются выход, истечение сессии, отключение, закрытие и
// удаление контакта, какой бы путь к ним ни привёл.
func (sub *Subscription) verify() error {
	ctx, cancel := context.WithTimeout(sub.ctx, storeTimeout)
	defer cancel()
	if err := sub.checkSession(ctx); err != nil {
		return sub.verifyError(err)
	}
	st, err := sub.svc.store.DeliveryState(ctx, sub.key.id, sub.key.userID)
	if err != nil {
		return sub.verifyError(err)
	}
	switch {
	case st.Closed:
		return store.ErrChatClosed
	case !st.Connected:
		return store.ErrNotConnected
	}
	return nil
}

// verifyError считает истечение собственного предела сверки недоступностью хранилища. Отмена означает, что
// поток уже завершается другой причиной.
func (sub *Subscription) verifyError(err error) error {
	if sub.ctx.Err() != nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return store.ErrUnavailable
	}
	return err
}

// consumerEnded определяет причину завершения потребителя. Отмена потребителя означает удаление очереди,
// поэтому состояние читается ещё раз, чтобы дать верную причину.
func (sub *Subscription) consumerEnded() {
	err := sub.consumer.Err()
	switch {
	case err == nil:
		return
	case errors.Is(err, ErrConsumerCancelled):
		if cause := sub.verify(); cause != nil {
			sub.terminate(cause, false)
			return
		}
		sub.terminate(ErrBrokerUnavailable, false)
	default:
		sub.terminate(err, false)
	}
}
