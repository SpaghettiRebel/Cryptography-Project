package delivery

import (
	"context"
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/localdb"
	"cryptochat/client/internal/rpcclient"
)

// ReceiveState возвращает состояние приёма чата и код причины для RETRYING.
func (e *Engine) ReceiveState(chat [16]byte) (state, reason string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.recv[chat]
	if !ok {
		return RecvOff, ""
	}
	return st.state, st.reason
}

// leaveRecv вызывается приёмником перед выходом: запись о нём удаляется вместе с записью конечного
// состояния, поэтому «Получать здесь» сразу после события запускает новый приёмник.
func (e *Engine) leaveRecv(chat [16]byte, w *worker, state string) {
	e.mu.Lock()
	if e.receivers[chat] == w {
		delete(e.receivers, chat)
	}
	prev := e.recv[chat]
	next := recvState{state: state}
	e.recv[chat] = next
	e.mu.Unlock()
	if prev != next {
		e.cfg.Events.ReceiveChanged(chat)
	}
}

func (e *Engine) setRecv(chat [16]byte, state, reason string) {
	e.mu.Lock()
	prev := e.recv[chat]
	next := recvState{state: state, reason: reason}
	e.recv[chat] = next
	e.mu.Unlock()
	if prev != next {
		e.cfg.Events.ReceiveChanged(chat)
	}
}

// Sync открывает приём активных чатов, где он нужен: по записанному желанию, а без записи — по
// подключению на сервере. Чат в состоянии ELSEWHERE сам не открывается. Заодно продолжаются ожидающие
// расшифрования и передачи.
func (e *Engine) Sync(active map[[16]byte]bool) {
	for id, selfConnected := range active {
		wanted, ok, err := e.cfg.Store.Wanted(id)
		if err != nil {
			continue
		}
		if !ok {
			wanted = selfConnected
		}
		if !wanted {
			continue
		}
		if st, _ := e.ReceiveState(id); st == RecvElsewhere {
			continue
		}
		e.startReceiver(id)
	}
	e.resume()
	e.wakeSenders()
}

// OpenReceiver записывает желание принимать чат и открывает поток; из ELSEWHERE тоже.
func (e *Engine) OpenReceiver(chat [16]byte) error {
	if err := e.cfg.Store.SetWanted(chat, true); err != nil {
		return err
	}
	if st, _ := e.ReceiveState(chat); st == RecvElsewhere {
		e.setRecv(chat, RecvOff, "")
	}
	e.startReceiver(chat)
	return nil
}

// CloseReceiver записывает отказ от приёма и закрывает поток без подтверждения несохранённого.
func (e *Engine) CloseReceiver(chat [16]byte) error {
	err := e.cfg.Store.SetWanted(chat, false)
	e.stopReceiver(chat)
	return err
}

func (e *Engine) startReceiver(chat [16]byte) {
	e.mu.Lock()
	if e.stopped || e.receivers[chat] != nil {
		e.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(e.ctx)
	w := &worker{cancel: cancel, done: make(chan struct{})}
	e.receivers[chat] = w
	e.mu.Unlock()
	if !e.goSafe("приём", func() {
		defer close(w.done)
		defer func() {
			e.mu.Lock()
			if e.receivers[chat] == w {
				delete(e.receivers, chat)
			}
			e.mu.Unlock()
		}()
		e.receive(ctx, chat, w)
	}) {
		cancel()
		close(w.done)
	}
}

func (e *Engine) stopReceiver(chat [16]byte) {
	e.mu.Lock()
	w := e.receivers[chat]
	e.mu.Unlock()
	if w != nil {
		w.cancel()
		<-w.done
	}
	e.setRecv(chat, RecvOff, "")
}

// receive держит поток чата открытым и переоткрывает его по таблице завершений.
func (e *Engine) receive(ctx context.Context, chat [16]byte, w *worker) {
	attempt := 0
	for ctx.Err() == nil {
		if st, _ := e.ReceiveState(chat); st != RecvRetrying {
			e.setRecv(chat, RecvOpening, "")
		}
		stream, err := e.cfg.API.OpenReceive(ctx, chat)
		if err == nil {
			attempt = 0
			e.setRecv(chat, RecvReceiving, "")
			e.wakeSender(chat)
			err = e.consume(ctx, chat, stream)
			_ = stream.Close()
		}
		if ctx.Err() != nil {
			e.leaveRecv(chat, w, RecvOff)
			return
		}
		code := apperr.CodeOf(err)
		e.log.Printf("приём: поток завершён: %s", code)
		switch code {
		case apperr.ChatClosed, apperr.ChatNotFound:
			e.leaveRecv(chat, w, RecvOff)
			e.cfg.Events.ChatClosed(chat)
			return
		case apperr.NotConnected:
			_ = e.cfg.Store.SetWanted(chat, false)
			e.leaveRecv(chat, w, RecvOff)
			return
		case apperr.SessionExpired:
			e.leaveRecv(chat, w, RecvOff)
			e.cfg.Events.SessionExpired()
			return
		case apperr.StreamReplaced:
			e.leaveRecv(chat, w, RecvElsewhere)
			e.cfg.Events.Notice("warning", apperr.StreamReplaced, "")
			return
		case apperr.LocalWrite:
			if attempt == 0 {
				e.localWrite("приём", err)
			}
		}
		e.setRecv(chat, RecvRetrying, code)
		if !e.wait(ctx, pause(retryPauses, attempt)) {
			e.leaveRecv(chat, w, RecvOff)
			return
		}
		attempt++
	}
}

type recvResult struct {
	d   rpcclient.Delivery
	err error
}

// consume читает поток пачками: до Prefetch фрагментов или batchWait тишины, запись одной транзакцией,
// подтверждение только после записи.
func (e *Engine) consume(ctx context.Context, chat [16]byte, stream rpcclient.ReceiveStream) error {
	in := make(chan recvResult)
	quit := make(chan struct{})
	defer close(quit)
	e.goSafe("чтение потока", func() {
		for {
			d, err := stream.Recv()
			select {
			case in <- recvResult{d, err}:
			case <-quit:
				return
			}
			if err != nil {
				return
			}
		}
	})
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var batch []rpcclient.Delivery
	for {
		var timeout <-chan time.Time
		if len(batch) > 0 {
			timeout = timer.C
		}
		select {
		case <-ctx.Done():
			// Несохранённое не подтверждается: сервер выдаст его снова.
			return ctx.Err()
		case r := <-in:
			if r.err != nil {
				return r.err
			}
			batch = append(batch, r.d)
			if len(batch) < rpcclient.ReceivePrefetch {
				timer.Reset(batchWait)
				continue
			}
		case <-timeout:
		}
		timer.Stop()
		if err := e.flush(ctx, chat, stream, batch); err != nil {
			return err
		}
		batch = nil
	}
}

// flush записывает пачку и подтверждает её. Паника обработки даёт INTERNAL: поток переоткрывается.
func (e *Engine) flush(ctx context.Context, chat [16]byte, stream rpcclient.ReceiveStream, batch []rpcclient.Delivery) (err error) {
	err = apperr.New(apperr.Internal)
	e.protect("обработка части", func() { err = e.storeBatch(ctx, chat, stream, batch) })
	return err
}

func (e *Engine) storeBatch(ctx context.Context, chat [16]byte, stream rpcclient.ReceiveStream, batch []rpcclient.Delivery) error {
	items := make([]localdb.Incoming, len(batch))
	for i, d := range batch {
		items[i] = localdb.Incoming{Wire: d.Wire, Frag: parse(d.Wire)}
	}
	res, err := e.cfg.Store.ReceiveBatch(chat, items, MaxTotal)
	if err != nil {
		return apperr.New(apperr.LocalWrite)
	}
	if ctx.Err() != nil {
		// Поток закрывается: записанное придёт снова и будет отброшено как повтор.
		return ctx.Err()
	}
	for _, d := range batch {
		if err := stream.Ack(d.ID); err != nil {
			return err
		}
	}
	var changed []int64
	for _, r := range res {
		if r.Dropped && !r.Corrupted {
			if r.Seq == 0 {
				e.log.Printf("приём: фрагмент отброшен: не разбирается, чужой чат или неверное число частей")
			}
			continue
		}
		changed = append(changed, r.Seq)
		if r.Completed {
			e.dec.enqueue(r.Seq)
		}
	}
	seen := map[int64]bool{}
	for _, s := range changed {
		if seen[s] {
			continue
		}
		seen[s] = true
		if m, ok, err := e.cfg.Store.Message(s); err == nil && ok {
			e.progress.Update(s, chat, StageReceive, int64(m.ReceivedCount), int64(m.Total))
		}
	}
	e.emitChanged(chat, changed...)
	return nil
}
