package delivery

import (
	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/localdb"
)

// CancelMessage отменяет живую операцию сообщения: шифрование и передачу исходящего (со служебной
// отменой, если части ушли или могли уйти), приём входящего (поток не закрывается) или его расшифрование.
func (e *Engine) CancelMessage(seq int64) (MessageView, error) {
	st := e.cfg.Store
	m, ok, err := st.Message(seq)
	if err != nil {
		return MessageView{}, apperr.New(apperr.LocalWrite)
	}
	if !ok || m.Kind == localdb.KindControl {
		return MessageView{}, apperr.New(apperr.MessageNotFound)
	}
	var (
		from []int
		to   int
	)
	switch {
	case m.Direction == localdb.DirOut:
		from, to = activeOut, localdb.StateCancelled
	case m.State == localdb.StateReceiving:
		from, to = []int{localdb.StateReceiving}, localdb.StateCancelled
	default:
		from, to = []int{localdb.StateDecryptPending, localdb.StateDecrypting}, localdb.StateDecryptCancelled
	}
	before := m
	changed, err := st.Transition(seq, from, to, "")
	if err != nil {
		return MessageView{}, apperr.New(apperr.LocalWrite)
	}
	if !changed {
		return MessageView{}, apperr.New(apperr.NotCancellable)
	}
	e.cancelOp(seq)
	e.progress.Forget(seq)
	e.forgetQueueNote(seq)
	m, _, err = st.Message(seq)
	if err != nil {
		return MessageView{}, apperr.New(apperr.LocalWrite)
	}
	if m.Direction == localdb.DirOut {
		e.forgetWriteFail(seq)
		if e.partsMayHaveLeft(before, m) {
			e.enqueueCancel(m.ChatID, m.MessageID)
		}
		e.wakeSender(m.ChatID)
	}
	v := e.View(m)
	e.cfg.Events.Messages(m.ChatID, []MessageView{v})
	return v, nil
}

// RetryDecrypt запускает расшифрование заново после отмены или ожидания с причиной.
func (e *Engine) RetryDecrypt(seq int64) (MessageView, error) {
	st := e.cfg.Store
	m, ok, err := st.Message(seq)
	if err != nil {
		return MessageView{}, apperr.New(apperr.LocalWrite)
	}
	if !ok || m.Kind == localdb.KindControl {
		return MessageView{}, apperr.New(apperr.MessageNotFound)
	}
	changed, err := st.Transition(seq, []int{localdb.StateDecryptCancelled, localdb.StateDecryptPending}, localdb.StateDecryptPending, "")
	if err != nil {
		return MessageView{}, apperr.New(apperr.LocalWrite)
	}
	if !changed || m.Direction != localdb.DirIn {
		return MessageView{}, apperr.New(apperr.NotRetryable)
	}
	e.dec.enqueue(seq)
	m, _, _ = st.Message(seq)
	v := e.View(m)
	e.cfg.Events.Messages(m.ChatID, []MessageView{v})
	return v, nil
}

// ChatClosed вызывается до удаления ключа закрытого или исчезнувшего чата: приём и передача чата
// останавливаются, незавершённые исходящие и принимаемые получают UNDELIVERED, ожидающие расшифрования —
// KEY_MISSING. Возвращает число затронутых сообщений.
func (e *Engine) ChatClosed(chat [16]byte) int {
	e.stopReceiver(chat)
	e.stopSender(chat)
	list, err := e.cfg.Store.Unfinished(chat)
	if err != nil {
		return 0
	}
	var changed []int64
	for _, m := range list {
		e.cancelOp(m.Seq)
		var (
			from []int
			to   int
			code string
		)
		switch {
		case m.Direction == localdb.DirOut:
			from, to = activeOut, localdb.StateUndelivered
		case m.State == localdb.StateReceiving:
			from, to = []int{localdb.StateReceiving}, localdb.StateUndelivered
		default:
			from = []int{localdb.StateDecryptPending, localdb.StateDecrypting, localdb.StateDecryptCancelled}
			to, code = localdb.StateKeyMissing, apperr.KeyMissing
		}
		if ok, _ := e.cfg.Store.Transition(m.Seq, from, to, code); ok {
			e.progress.Forget(m.Seq)
			if m.Kind != localdb.KindControl {
				changed = append(changed, m.Seq)
			}
		}
	}
	e.emitChanged(chat, changed...)
	return len(changed)
}
