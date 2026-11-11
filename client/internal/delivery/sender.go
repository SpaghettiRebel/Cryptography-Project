package delivery

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/ciphers"
	"cryptochat/client/internal/localdb"
	"cryptochat/core/message"
	"cryptochat/core/protocol"
)

// sender — передатчик чата: сообщения уходят строго по очереди, служебные впереди.
type sender struct {
	worker
	wake chan struct{}
	sent chan struct{} // часть отправлена: шифрование может продолжить
}

func (e *Engine) startSender(chat [16]byte) {
	e.mu.Lock()
	if e.stopped || e.senders[chat] != nil {
		s := e.senders[chat]
		e.mu.Unlock()
		if s != nil {
			signal(s.wake)
		}
		return
	}
	ctx, cancel := context.WithCancel(e.ctx)
	s := &sender{worker: worker{cancel: cancel, done: make(chan struct{})}, wake: make(chan struct{}, 1),
		sent: make(chan struct{}, 1)}
	e.senders[chat] = s
	e.mu.Unlock()
	if !e.goSafe("отправка", func() {
		defer close(s.done)
		defer func() {
			e.mu.Lock()
			if e.senders[chat] == s {
				delete(e.senders, chat)
			}
			e.mu.Unlock()
		}()
		e.transmitChat(ctx, chat, s)
	}) {
		cancel()
		close(s.done)
	}
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (e *Engine) wakeSender(chat [16]byte) {
	e.mu.Lock()
	s := e.senders[chat]
	e.mu.Unlock()
	if s != nil {
		signal(s.wake)
	}
}

func (e *Engine) wakeSenders() {
	e.mu.Lock()
	list := make([]*sender, 0, len(e.senders))
	for _, s := range e.senders {
		list = append(list, s)
	}
	e.mu.Unlock()
	for _, s := range list {
		signal(s.wake)
	}
}

func (e *Engine) stopSender(chat [16]byte) {
	e.mu.Lock()
	s := e.senders[chat]
	e.mu.Unlock()
	if s != nil {
		s.cancel()
		<-s.done
	}
}

// waitWake ждёт сигнала передатчику, отмены или срока d.
func (e *Engine) waitWake(ctx context.Context, s *sender, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-s.wake:
		return true
	case <-t.C:
		return true
	}
}

// transmitChat передаёт сообщения чата по одному, пока доставка работает.
func (e *Engine) transmitChat(ctx context.Context, chat [16]byte, s *sender) {
	for ctx.Err() == nil {
		m, ok, err := e.cfg.Store.NextOutgoing(chat)
		if err != nil {
			if !e.waitWake(ctx, s, time.Second) {
				return
			}
			continue
		}
		if !ok {
			if !e.waitWake(ctx, s, time.Minute) {
				return
			}
			continue
		}
		e.transmit(ctx, chat, s, m)
	}
}

// transmit передаёт одно сообщение: шифрование идёт параллельно и пишет части в спул, передача берёт
// их по порядку по мере появления.
func (e *Engine) transmit(parent context.Context, chat [16]byte, s *sender, m localdb.Message) {
	ctx, done := e.opContext(parent, m.Seq)
	defer done()
	seq := m.Seq
	attempt := 0
	for ctx.Err() == nil {
		m, ok, err := e.cfg.Store.Message(seq)
		if err != nil {
			e.waitWake(ctx, s, time.Second)
			continue
		}
		if !ok || localdb.Final(m.State) || m.SentCount >= m.Total {
			return
		}
		wire, have, err := e.cfg.Store.OutFragment(seq, m.SentCount)
		if err != nil {
			e.waitWake(ctx, s, time.Second)
			continue
		}
		if !have {
			if m.EncryptedCount <= m.SentCount && !e.encryptingNow(seq) {
				e.startEncrypt(ctx, chat, s, m)
			}
			e.waitWake(ctx, s, 2*time.Second)
			continue
		}
		wasMaybe := e.setMaybeSent(seq, true)
		err = e.cfg.API.SendFragment(ctx, chat, wire)
		if ctx.Err() != nil {
			return
		}
		if err != nil && surelyNotSent(apperr.CodeOf(err)) {
			e.setMaybeSent(seq, wasMaybe)
		}
		if err == nil {
			attempt = 0
			next, ok, err := e.cfg.Store.FragmentSent(seq, m.SentCount)
			if err != nil {
				e.localWrite("отправка", err)
				e.waitWake(ctx, s, time.Second)
				continue
			}
			if ok {
				signal(s.sent)
				e.progress.Update(seq, chat, StageSend, int64(next.SentCount), int64(next.Total))
				if next.State == localdb.StateSent || m.State == localdb.StateWaiting || next.SentCount == 1 {
					e.emitChanged(chat, seq)
				}
				if next.State == localdb.StateSent {
					e.progress.Forget(seq)
					e.forgetQueueNote(seq)
					e.takeMaybeSent(seq)
					return
				}
			}
			continue
		}
		code := apperr.CodeOf(err)
		switch code {
		case apperr.QueueFull:
			e.setWaiting(chat, m, code)
			e.noteQueueFull(seq)
			if !e.wait(ctx, pause(queuePauses, attempt)) {
				return
			}
			attempt++
		case apperr.ChatNotReady, apperr.ServerBusy, apperr.ServerUnavailable, apperr.Timeout, apperr.CertInvalid:
			e.setWaiting(chat, m, code)
			if !e.wait(ctx, pause(retryPauses, attempt)) {
				return
			}
			attempt++
		case apperr.NotConnected:
			// Передача продолжится, когда приём чата снова открыт.
			e.setWaiting(chat, m, code)
			if !e.waitWake(ctx, s, 5*time.Second) {
				return
			}
		case apperr.ChatClosed, apperr.ChatNotFound:
			if ok, _ := e.cfg.Store.Transition(seq, activeOut, localdb.StateUndelivered, ""); ok {
				e.emitChanged(chat, seq)
			}
			e.cfg.Events.ChatClosed(chat)
			return
		case apperr.SessionExpired:
			// Состояние не меняется: после входа передача продолжится.
			e.cfg.Events.SessionExpired()
			e.wait(ctx, 5*time.Second)
			return
		default:
			e.fail(chat, seq, apperr.Internal)
			return
		}
	}
}

var activeOut = []int{localdb.StateQueued, localdb.StateSending, localdb.StateWaiting}

func (e *Engine) setWaiting(chat [16]byte, m localdb.Message, code string) {
	if m.State == localdb.StateWaiting && m.ErrorCode == code {
		return
	}
	if ok, _ := e.cfg.Store.SetWaiting(m.Seq, code); ok {
		e.emitChanged(chat, m.Seq)
	}
}

func (e *Engine) noteQueueFull(seq int64) {
	e.mu.Lock()
	first := !e.queueNote[seq]
	e.queueNote[seq] = true
	e.mu.Unlock()
	if first {
		e.cfg.Events.Notice("warning", apperr.QueueFull, "")
	}
}

func (e *Engine) forgetQueueNote(seq int64) {
	e.mu.Lock()
	delete(e.queueNote, seq)
	e.mu.Unlock()
}

// fail переводит исходящее в FAILED; если части уже ушли или могли уйти, получатель получит служебную
// отмену.
func (e *Engine) fail(chat [16]byte, seq int64, code string) {
	before, _, _ := e.cfg.Store.Message(seq)
	ok, err := e.cfg.Store.Transition(seq, activeOut, localdb.StateFailed, code)
	if err != nil || !ok {
		return
	}
	e.progress.Forget(seq)
	e.cancelOp(seq)
	e.forgetWriteFail(seq)
	if m, ok, _ := e.cfg.Store.Message(seq); ok && e.partsMayHaveLeft(before, m) {
		e.enqueueCancel(chat, m.MessageID)
	}
	e.emitChanged(chat, seq)
}

// surelyNotSent — ответ на отправку части, при котором сервер её точно не опубликовал.
func surelyNotSent(code string) bool {
	switch code {
	case apperr.QueueFull, apperr.ChatNotReady, apperr.NotConnected, apperr.ChatClosed, apperr.ChatNotFound,
		apperr.SessionExpired, apperr.ServerBusy, apperr.CertInvalid:
		return true
	}
	return false
}

func (e *Engine) setMaybeSent(seq int64, on bool) (was bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	was = e.maybeSent[seq]
	if on {
		e.maybeSent[seq] = true
	} else {
		delete(e.maybeSent, seq)
	}
	return was
}

func (e *Engine) takeMaybeSent(seq int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	was := e.maybeSent[seq]
	delete(e.maybeSent, seq)
	return was
}

// partsMayHaveLeft сообщает, что части исходящего могли дойти до сервера: часть отправлена, отправка
// прервана с неизвестным исходом или исходящее ждало повтора после такого ответа (before — строка до
// перевода в конечное состояние). Лишняя служебная отмена у получателя безвредна.
func (e *Engine) partsMayHaveLeft(before, after localdb.Message) bool {
	maybe := e.takeMaybeSent(after.Seq)
	if after.SentCount > 0 || maybe {
		return true
	}
	return before.State == localdb.StateWaiting && before.ErrorCode != "" && !surelyNotSent(before.ErrorCode)
}

func (e *Engine) encryptingNow(seq int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.encoding[seq]
}

// startEncrypt запускает шифрование сообщения с первой незашифрованной части.
func (e *Engine) startEncrypt(ctx context.Context, chat [16]byte, s *sender, m localdb.Message) {
	e.mu.Lock()
	if e.encoding[m.Seq] {
		e.mu.Unlock()
		return
	}
	e.encoding[m.Seq] = true
	e.mu.Unlock()
	if !e.goSafe("шифрование", func() {
		defer func() {
			e.mu.Lock()
			delete(e.encoding, m.Seq)
			e.mu.Unlock()
			signal(s.wake)
		}()
		e.encrypt(ctx, chat, s, m)
	}) {
		e.mu.Lock()
		delete(e.encoding, m.Seq)
		e.mu.Unlock()
	}
}

// errStale — сообщение отменено или завершено во время шифрования.
var errStale = errors.New("сообщение больше не отправляется")

// sourceError помечает ошибку чтения исходного файла.
type sourceError struct{ err error }

func (s sourceError) Error() string { return "исходный файл: " + s.err.Error() }
func (s sourceError) Unwrap() error { return s.err }

type sourceReader struct{ r io.Reader }

func (s sourceReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil && err != io.EOF {
		err = sourceError{err}
	}
	return n, err
}

func (e *Engine) encrypt(ctx context.Context, chat [16]byte, s *sender, m localdb.Message) {
	select {
	case e.encSem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-e.encSem }()

	info, ok := e.cfg.Chat(chat)
	if !ok {
		if ok, _ := e.cfg.Store.Transition(m.Seq, activeOut, localdb.StateUndelivered, ""); ok {
			e.emitChanged(chat, m.Seq)
		}
		return
	}
	impl, err := ciphers.Resolve(info.Algorithm)
	if err != nil {
		e.fail(chat, m.Seq, apperr.AlgorithmUnavailable)
		return
	}
	key, ok, err := e.cfg.Store.SessionKey(chat)
	if err != nil {
		e.encryptWriteFailed(ctx, m.Seq, err)
		return
	}
	if !ok {
		e.fail(chat, m.Seq, apperr.KeyMissing)
		return
	}
	meta, src, closeSrc, code := e.source(m)
	if code != "" {
		e.fail(chat, m.Seq, code)
		return
	}
	defer closeSrc()
	skip := m.EncryptedCount
	emit := func(f *protocol.Fragment) error {
		if int(f.Index) < skip {
			return nil
		}
		if err := e.waitSendRoom(ctx, s, m.Seq, int(f.Index)); err != nil {
			return err
		}
		wire, err := f.Marshal()
		if err != nil {
			return err
		}
		ok, err := e.cfg.Store.AddOutFragment(m.Seq, int(f.Index), wire)
		if err != nil {
			return storeError{err}
		}
		if !ok {
			return errStale
		}
		signal(s.wake)
		return nil
	}
	progress := func(done, total int64) { e.progress.Update(m.Seq, chat, StageEncrypt, done, total) }
	err = message.Seal(ctx, params(info, impl, key), m.MessageID, meta, src, emit, progress)
	switch {
	case err == nil:
		e.forgetWriteFail(m.Seq)
		if m.Kind == localdb.KindFile && !m.ChunksComplete && m.SourcePath != "" && !sameSource(m) {
			e.fail(chat, m.Seq, apperr.SourceChanged)
		}
	case ctx.Err() != nil, errors.Is(err, errStale), errors.Is(err, localdb.ErrStale):
	case isStoreError(err):
		e.encryptWriteFailed(ctx, m.Seq, err)
	case errors.Is(err, message.ErrSizeMismatch), isSourceError(err):
		e.fail(chat, m.Seq, apperr.SourceChanged)
	default:
		e.log.Printf("шифрование: ошибка %T", err)
		e.fail(chat, m.Seq, apperr.Internal)
	}
}

// encryptWriteFailed обрабатывает ошибку записи в базу при шифровании: уведомление — один раз на
// сообщение, пауза перед следующей попыткой растёт по retryPauses.
func (e *Engine) encryptWriteFailed(ctx context.Context, seq int64, err error) {
	e.mu.Lock()
	n := e.writeFail[seq]
	e.writeFail[seq] = n + 1
	e.mu.Unlock()
	if n == 0 {
		e.localWrite("шифрование", err)
	} else {
		e.log.Printf("доставка: шифрование: запись в базу профиля снова не удалась")
	}
	e.wait(ctx, pause(retryPauses, n))
}

func (e *Engine) forgetWriteFail(seq int64) {
	e.mu.Lock()
	delete(e.writeFail, seq)
	e.mu.Unlock()
}

// waitSendRoom ждёт, пока часть idx окажется не дальше encryptAhead от первой неотправленной: спул
// шифротекста в базе не растёт при медленной отправке, а чтение исходного файла приостанавливается.
func (e *Engine) waitSendRoom(ctx context.Context, s *sender, seq int64, idx int) error {
	for {
		cur, ok, err := e.cfg.Store.Message(seq)
		if err != nil {
			return storeError{err}
		}
		if !ok || localdb.Final(cur.State) {
			return errStale
		}
		if idx < cur.SentCount+encryptAhead {
			return nil
		}
		t := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-s.sent:
		case <-t.C:
		}
		t.Stop()
	}
}

func isSourceError(err error) bool {
	var s sourceError
	return errors.As(err, &s)
}

// sameSource сверяет размер и время изменения исходного файла с запомненными при выборе.
func sameSource(m localdb.Message) bool {
	st, err := os.Stat(m.SourcePath)
	return err == nil && st.Size() == m.SourceSize && st.ModTime().UnixNano() == m.SourceMtime
}

// source возвращает метаданные и открытые данные сообщения: текст из базы, файл из копии в базе или
// исходный файл, если он не изменился. Код ошибки не пуст — продолжить нельзя.
func (e *Engine) source(m localdb.Message) (message.Meta, io.Reader, func(), string) {
	nop := func() {}
	switch {
	case m.Kind == localdb.KindText:
		return TextMeta(m.Text), strings.NewReader(m.Text), nop, ""
	case m.Kind == localdb.KindControl:
		return message.Meta{Type: CancelType, TotalSize: m.Size}, &chunkReader{store: e.cfg.Store, seq: m.Seq, after: -1}, nop, ""
	case m.ChunksComplete:
		meta := message.Meta{Name: m.FileName, Type: m.FileType, TotalSize: m.Size}
		return meta, &chunkReader{store: e.cfg.Store, seq: m.Seq, after: -1}, nop, ""
	}
	if m.SourcePath == "" || !sameSource(m) {
		return message.Meta{}, nil, nop, apperr.SourceChanged
	}
	f, err := os.Open(m.SourcePath)
	if err != nil {
		return message.Meta{}, nil, nop, apperr.SourceChanged
	}
	meta := message.Meta{Name: m.FileName, Type: m.FileType, TotalSize: m.Size}
	tee := &chunkTee{store: e.cfg.Store, seq: m.Seq, size: m.Size}
	return meta, io.TeeReader(sourceReader{f}, tee), func() { _ = f.Close() }, ""
}

// chunkTee копирует открытое содержимое файла в куски истории по мере чтения.
type chunkTee struct {
	store   Store
	seq     int64
	size    int64
	buf     []byte
	idx     int
	written int64
}

func (t *chunkTee) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	t.written += int64(len(p))
	for len(t.buf) >= localdb.ChunkSize || (t.written >= t.size && len(t.buf) > 0) {
		n := min(len(t.buf), localdb.ChunkSize)
		last := t.written >= t.size && n == len(t.buf)
		if err := t.store.PutChunk(t.seq, t.idx, t.buf[:n], last); err != nil {
			if errors.Is(err, localdb.ErrStale) {
				return 0, errStale
			}
			return 0, storeError{err}
		}
		t.idx++
		t.buf = append(t.buf[:0], t.buf[n:]...)
	}
	return len(p), nil
}

// chunkReader читает куски сообщения по порядку пачками.
type chunkReader struct {
	store Store
	seq   int64
	after int
	queue []localdb.Chunk
	cur   []byte
}

func (r *chunkReader) Read(p []byte) (int, error) {
	for len(r.cur) == 0 {
		if len(r.queue) == 0 {
			list, err := r.store.Chunks(r.seq, r.after, localdb.BatchRows)
			if err != nil {
				return 0, storeError{err}
			}
			if len(list) == 0 {
				return 0, io.EOF
			}
			r.queue = list
			r.after = list[len(list)-1].Idx
		}
		r.cur, r.queue = r.queue[0].Data, r.queue[1:]
	}
	n := copy(p, r.cur)
	r.cur = r.cur[n:]
	return n, nil
}

// SendText ставит текст в очередь чата. Проверки длины и доступности отправки — у вызывающего.
func (e *Engine) SendText(chat [16]byte, text string) (MessageView, error) {
	return e.queue(localdb.Message{ChatID: chat, Kind: localdb.KindText, Text: text, Size: int64(len(text)),
		Total: fragmentCount(int64(len(text)))}, nil)
}

// SendFile ставит файл в очередь чата; путь, размер и время изменения запоминаются для продолжения.
func (e *Engine) SendFile(chat [16]byte, path string, size int64, mtime time.Time) (MessageView, error) {
	meta := FileMeta(path, size)
	return e.queue(localdb.Message{ChatID: chat, Kind: localdb.KindFile, FileName: meta.Name, FileType: meta.Type,
		Size: size, Total: fragmentCount(size), SourcePath: path, SourceSize: size, SourceMtime: mtime.UnixNano(),
		ChunksComplete: size == 0}, nil)
}

func (e *Engine) queue(m localdb.Message, first []byte) (MessageView, error) {
	id, err := newMessageID()
	if err != nil {
		return MessageView{}, apperr.New(apperr.Internal)
	}
	m.MessageID = id
	seq, err := e.cfg.Store.CreateOutgoing(m, first)
	if err != nil {
		e.localWrite("очередь", err)
		return MessageView{}, apperr.New(apperr.LocalWrite)
	}
	e.startSender(m.ChatID)
	stored, ok, err := e.cfg.Store.Message(seq)
	if err != nil || !ok {
		return MessageView{}, apperr.New(apperr.LocalWrite)
	}
	v := e.View(stored)
	if m.Kind != localdb.KindControl {
		e.cfg.Events.Messages(m.ChatID, []MessageView{v})
	}
	return v, nil
}

// enqueueCancel ставит впереди очереди чата служебное сообщение отмены сообщения target.
func (e *Engine) enqueueCancel(chat, target [16]byte) {
	meta, data := CancelPayload(target)
	if _, err := e.queue(localdb.Message{ChatID: chat, Kind: localdb.KindControl, FileType: meta.Type, Size: 16, Total: 1,
		ChunksComplete: true}, data); err != nil {
		e.log.Printf("отмена: служебное сообщение не поставлено в очередь")
	}
}
