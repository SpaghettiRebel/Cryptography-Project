package apitest

import (
	"context"
	"slices"
	"strings"
	"sync"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/rpcclient"
)

// Prefetch — сколько неподтверждённых фрагментов поток выдаёт, как сервер.
const Prefetch = 16

// Доставка в подделке устроена как на сервере: очередь на пару (чат, получатель), поток на пару, выдача
// не больше Prefetch неподтверждённых, возврат неподтверждённого в очередь при завершении потока с
// признаком повторной выдачи.

type qkey struct {
	id   [16]byte
	user string
}

type item struct {
	uid         uint64
	wire        []byte
	redelivered bool
	issued      bool
}

type stream struct {
	srv      *Server
	key      qkey
	err      error
	inflight map[uint64]*item
	closed   bool // закрыт клиентом
}

// deliveryState — часть Server для доставки; поля под Server.mu.
type deliveryState struct {
	cond       *sync.Cond
	queues     map[qkey][]*item
	streams    map[qkey]*stream
	nextUID    uint64
	queueLimit int
	duplicate  bool
	picker     func(n int) int
	paused     map[qkey]bool
	onAck      func(id [16]byte, login string, uid uint64)
	acks       []uint64
	published  int
}

func (s *Server) dlv() *deliveryState {
	if s.d == nil {
		s.d = &deliveryState{cond: sync.NewCond(&s.mu), queues: map[qkey][]*item{}, streams: map[qkey]*stream{},
			paused: map[qkey]bool{}}
	}
	return s.d
}

func key(id [16]byte, login string) qkey { return qkey{id, strings.ToLower(login)} }

// SetQueueLimit задаёт наибольшее число фрагментов в очереди получателя; больше — QUEUE_FULL. 0 — без предела.
func (s *Server) SetQueueLimit(n int) {
	s.mu.Lock()
	s.dlv().queueLimit = n
	s.mu.Unlock()
}

// SetDuplicate включает повторную публикацию: каждый фрагмент попадает в очередь дважды.
func (s *Server) SetDuplicate(on bool) {
	s.mu.Lock()
	s.dlv().duplicate = on
	s.mu.Unlock()
}

// SetPicker задаёт порядок выдачи: функция выбирает номер из n готовых к выдаче фрагментов очереди
// (0 — самый старый). nil — по порядку.
func (s *Server) SetPicker(f func(n int) int) {
	s.mu.Lock()
	s.dlv().picker = f
	s.mu.Unlock()
}

// Pause останавливает или возобновляет выдачу очереди получателя.
func (s *Server) Pause(id [16]byte, login string, on bool) {
	s.mu.Lock()
	s.dlv().paused[key(id, login)] = on
	s.mu.Unlock()
	s.dlv().cond.Broadcast()
}

// OnAck задаёт крючок, который вызывается при каждом подтверждении до удаления фрагмента из очереди.
func (s *Server) OnAck(f func(id [16]byte, login string, uid uint64)) {
	s.mu.Lock()
	s.dlv().onAck = f
	s.mu.Unlock()
}

// Acks возвращает номера подтверждённых фрагментов.
func (s *Server) Acks() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.dlv().acks)
}

// QueueLen — число фрагментов в очереди получателя, включая выданные и неподтверждённые.
func (s *Server) QueueLen(id [16]byte, login string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.dlv().queues[key(id, login)])
}

// Published — число принятых публикаций.
func (s *Server) Published() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dlv().published
}

// Inject кладёт произвольные байты в очередь получателя, минуя проверки отправки.
func (s *Server) Inject(id [16]byte, login string, wire []byte) {
	s.mu.Lock()
	s.enqueueLocked(key(id, login), wire)
	s.mu.Unlock()
}

// Streaming сообщает, что у получателя открыт поток чата.
func (s *Server) Streaming(id [16]byte, login string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.dlv().streams[key(id, login)]
	return ok
}

// EndStream завершает поток получателя с ошибкой err, как сервер.
func (s *Server) EndStream(id [16]byte, login string, err error) {
	s.mu.Lock()
	if st := s.dlv().streams[key(id, login)]; st != nil {
		s.endLocked(st, err)
	}
	s.mu.Unlock()
}

// endAllLocked завершает все потоки.
func (s *Server) endAllLocked(err error) {
	for _, st := range s.dlv().streams {
		s.endLocked(st, err)
	}
}

// dropChatLocked завершает потоки чата и удаляет его очереди (закрытие чата).
func (s *Server) dropChatLocked(id [16]byte) {
	d := s.dlv()
	for k, st := range d.streams {
		if k.id == id {
			s.endLocked(st, apperr.New(apperr.ChatClosed))
		}
	}
	for k := range d.queues {
		if k.id == id {
			delete(d.queues, k)
		}
	}
}

func (s *Server) enqueueLocked(k qkey, wire []byte) {
	d := s.dlv()
	n := 1
	if d.duplicate {
		n = 2
	}
	for i := 0; i < n; i++ {
		d.nextUID++
		d.queues[k] = append(d.queues[k], &item{uid: d.nextUID, wire: slices.Clone(wire)})
	}
	d.published++
	d.cond.Broadcast()
}

// endLocked завершает поток: неподтверждённое возвращается в очередь с признаком повторной выдачи.
// Закрытие клиентом снимает подключение участника, как на сервере.
func (s *Server) endLocked(st *stream, err error) {
	if st.err != nil {
		return
	}
	st.err = err
	for _, it := range st.inflight {
		it.issued = false
		it.redelivered = true
	}
	st.inflight = nil
	d := s.dlv()
	if d.streams[st.key] == st {
		delete(d.streams, st.key)
		if st.closed {
			if ch := s.chats[st.key.id]; ch != nil {
				ch.conn[st.key.user] = false
			}
		}
	}
	d.cond.Broadcast()
}

// SetSendHook задаёт поведение SendFragment этого соединения: f получает номер вызова (с 1) и байты и
// возвращает, публиковать ли фрагмент, и ошибку для клиента. Например, (true, TIMEOUT) — публикация прошла,
// а ответ потерян.
func (c *Client) SetSendHook(f func(n int, wire []byte) (publish bool, err error)) {
	c.mu.Lock()
	c.sendHook = f
	c.mu.Unlock()
}

func (c *Client) SendFragment(ctx context.Context, chatID [16]byte, wire []byte) error {
	if err := c.enter(ctx, "SendFragment"); err != nil {
		return err
	}
	c.mu.Lock()
	c.sends++
	n, hook := c.sends, c.sendHook
	c.mu.Unlock()
	s := c.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, me, err := c.member(chatID)
	if err != nil {
		return err
	}
	peer := ch.members[0]
	if peer == me {
		peer = ch.members[1]
	}
	switch {
	case !ch.closed.IsZero():
		return apperr.New(apperr.ChatClosed)
	case !ch.conn[me]:
		return apperr.New(apperr.NotConnected)
	case ch.values[me] == nil || ch.values[peer] == nil:
		return apperr.New(apperr.ChatNotReady)
	case len(wire) == 0 || len(wire) > 61440:
		return apperr.New(apperr.Internal)
	}
	publish, herr := true, error(nil)
	if hook != nil {
		s.mu.Unlock()
		publish, herr = hook(n, wire)
		s.mu.Lock()
	}
	k := key(chatID, peer)
	if publish {
		if lim := s.dlv().queueLimit; lim > 0 && len(s.dlv().queues[k]) >= lim {
			return apperr.New(apperr.QueueFull)
		}
		s.enqueueLocked(k, wire)
	}
	return herr
}

func (c *Client) OpenReceive(ctx context.Context, chatID [16]byte) (rpcclient.ReceiveStream, error) {
	if err := c.enter(ctx, "OpenReceive"); err != nil {
		return nil, err
	}
	s := c.srv
	s.mu.Lock()
	ch, me, err := c.member(chatID)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if !ch.closed.IsZero() {
		s.mu.Unlock()
		return nil, apperr.New(apperr.ChatClosed)
	}
	ch.conn[me] = true
	k := key(chatID, me)
	d := s.dlv()
	if old := d.streams[k]; old != nil {
		s.endLocked(old, apperr.New(apperr.StreamReplaced))
	}
	st := &stream{srv: s, key: k, inflight: map[uint64]*item{}}
	d.streams[k] = st
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		st.closed = true
		s.endLocked(st, apperr.New(apperr.Internal))
		s.mu.Unlock()
	}()
	return &clientStream{st: st, cancel: cancel}, nil
}

type clientStream struct {
	st     *stream
	cancel context.CancelFunc
}

func (cs *clientStream) Recv() (rpcclient.Delivery, error) {
	st := cs.st
	s := st.srv
	d := s.dlv()
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if st.err != nil {
			return rpcclient.Delivery{}, st.err
		}
		if !d.paused[st.key] && len(st.inflight) < Prefetch {
			var ready []*item
			for _, it := range d.queues[st.key] {
				if !it.issued {
					ready = append(ready, it)
				}
			}
			if len(ready) > 0 {
				i := 0
				if d.picker != nil {
					i = d.picker(len(ready))
				}
				it := ready[i]
				it.issued = true
				st.inflight[it.uid] = it
				return rpcclient.Delivery{ID: it.uid, Wire: slices.Clone(it.wire), Redelivered: it.redelivered}, nil
			}
		}
		d.cond.Wait()
	}
}

func (cs *clientStream) Ack(uid uint64) error {
	st := cs.st
	s := st.srv
	d := s.dlv()
	s.mu.Lock()
	if st.err != nil || st.inflight[uid] == nil {
		s.mu.Unlock()
		return nil
	}
	hook := d.onAck
	s.mu.Unlock()
	if hook != nil {
		hook(st.key.id, st.key.user, uid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.err != nil || st.inflight[uid] == nil {
		return nil
	}
	delete(st.inflight, uid)
	q := d.queues[st.key]
	for i, it := range q {
		if it.uid == uid {
			d.queues[st.key] = append(q[:i:i], q[i+1:]...)
			break
		}
	}
	d.acks = append(d.acks, uid)
	d.cond.Broadcast()
	return nil
}

func (cs *clientStream) Close() error {
	s := cs.st.srv
	s.mu.Lock()
	cs.st.closed = true
	s.endLocked(cs.st, apperr.New(apperr.Internal))
	s.mu.Unlock()
	cs.cancel()
	return nil
}
