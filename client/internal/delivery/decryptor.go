package delivery

import (
	"context"
	"errors"
	"io"
	"sync"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/ciphers"
	"cryptochat/client/internal/keyagree"
	"cryptochat/client/internal/localdb"
	"cryptochat/core/message"
)

// wrapWriter подменяется в тестах, чтобы считать одновременные вызовы WriteAt.
var wrapWriter = func(w io.WriterAt) io.WriterAt { return w }

// decryptor — очередь расшифрования: не больше decryptSlots сообщений одновременно.
// Сообщение, которое ставят в очередь во время его расшифрования (например, повтор сразу после
// отмены), помечается again и ставится снова после завершения текущего прохода.
type decryptor struct {
	e       *Engine
	mu      sync.Mutex
	queue   []int64
	queued  map[int64]bool
	running map[int64]bool
	again   map[int64]bool
	signal  chan struct{}
}

func newDecryptor(e *Engine) *decryptor {
	return &decryptor{e: e, queued: map[int64]bool{}, running: map[int64]bool{}, again: map[int64]bool{},
		signal: make(chan struct{}, decryptSlots)}
}

func (d *decryptor) enqueue(seq int64) {
	d.mu.Lock()
	if d.queued[seq] {
		if d.running[seq] {
			d.again[seq] = true
		}
		d.mu.Unlock()
		return
	}
	d.queued[seq] = true
	d.queue = append(d.queue, seq)
	d.mu.Unlock()
	d.wake()
}

func (d *decryptor) wake() {
	for i := 0; i < decryptSlots; i++ {
		select {
		case d.signal <- struct{}{}:
		default:
		}
	}
}

func (d *decryptor) pop() (int64, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.queue) == 0 {
		return 0, false
	}
	s := d.queue[0]
	d.queue = d.queue[1:]
	d.running[s] = true
	return s, true
}

func (d *decryptor) done(seq int64) {
	d.mu.Lock()
	delete(d.running, seq)
	repeat := d.again[seq]
	delete(d.again, seq)
	if repeat {
		d.queue = append(d.queue, seq)
	} else {
		delete(d.queued, seq)
	}
	d.mu.Unlock()
	if repeat {
		d.wake()
	}
}

func (d *decryptor) run() {
	ctx := d.e.ctx
	for ctx.Err() == nil {
		seq, ok := d.pop()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-d.signal:
			}
			continue
		}
		d.e.protect("расшифрование сообщения", func() { d.e.decrypt(seq) })
		d.done(seq)
	}
}

// storeWriter помечает ошибки записи кусков как ошибки базы.
type storeWriter struct{ w io.WriterAt }

func (s storeWriter) WriteAt(p []byte, off int64) (int, error) {
	n, err := s.w.WriteAt(p, off)
	if err != nil && !errors.Is(err, localdb.ErrStale) {
		err = storeError{err}
	}
	return n, err
}

// decrypt расшифровывает сообщение, все части которого приняты.
func (e *Engine) decrypt(seq int64) {
	st := e.cfg.Store
	m, ok, err := st.Message(seq)
	if err != nil || !ok || m.State != localdb.StateDecryptPending || m.ErrorCode != "" && m.ErrorCode != apperr.AlgorithmUnavailable {
		return
	}
	chat := m.ChatID
	info, active := e.cfg.Chat(chat)
	if !active {
		e.keyMissing(m)
		return
	}
	switch info.KeyState {
	case keyagree.KeyLost, keyagree.PeerValueInvalid:
		e.keyMissing(m)
		return
	case keyagree.Ready:
	default:
		return // ключ ещё согласуется
	}
	impl, err := ciphers.Resolve(info.Algorithm)
	if err != nil {
		if m.ErrorCode != apperr.AlgorithmUnavailable {
			if ok, _ := st.Transition(seq, []int{localdb.StateDecryptPending}, localdb.StateDecryptPending, apperr.AlgorithmUnavailable); ok {
				e.emitChanged(chat, seq)
			}
		}
		return
	}
	key, ok, err := st.SessionKey(chat)
	if err != nil || !ok {
		return
	}
	if ok, err := st.Transition(seq, []int{localdb.StateDecryptPending}, localdb.StateDecrypting, ""); err != nil || !ok {
		return
	}
	e.emitChanged(chat, seq)
	ctx, done := e.opContext(e.ctx, seq)
	defer done()
	progress := func(d, t int64) { e.progress.Update(seq, chat, StageDecrypt, d, t) }
	asm, err := message.NewAssembler(params(info, impl, key), m.MessageID, wrapWriter(storeWriter{st.ChunkWriterAt(seq)}), progress)
	if err != nil {
		// Неверные параметры чата (длина ключа, номер режима) — не повреждение сообщения: части
		// остаются, сообщение ждёт повтора с причиной.
		e.log.Printf("расшифрование: параметры чата не приняты")
		_, _ = st.Transition(seq, []int{localdb.StateDecrypting}, localdb.StateDecryptPending, apperr.InvalidChatParams)
		e.progress.Forget(seq)
		e.emitChanged(chat, seq)
		return
	}
	err = e.assemble(ctx, seq, asm)
	var meta message.Meta
	if err == nil {
		meta, err = asm.Finish()
	}
	if err == nil {
		err = e.finish(chat, seq, meta)
	}
	switch {
	case err == nil:
	case e.ctx.Err() != nil:
		// Доставка останавливается: после входа расшифрование начнётся заново.
		_, _ = st.Transition(seq, []int{localdb.StateDecrypting}, localdb.StateDecryptPending, "")
	case ctx.Err() != nil, errors.Is(err, localdb.ErrStale):
		// Отменено пользователем, отправителем или закрытием чата: состояние уже записано.
	case isStoreError(err):
		e.localWrite("расшифрование", err)
		_, _ = st.Transition(seq, []int{localdb.StateDecrypting}, localdb.StateDecryptPending, "")
	default:
		if ok, _ := st.Transition(seq, []int{localdb.StateDecrypting}, localdb.StateCorrupted, apperr.MessageCorrupted); ok {
			e.log.Printf("расшифрование: сообщение повреждено")
		}
	}
	e.progress.Forget(seq)
	e.emitChanged(chat, seq)
}

func (e *Engine) keyMissing(m localdb.Message) {
	if ok, _ := e.cfg.Store.Transition(m.Seq, []int{localdb.StateDecryptPending, localdb.StateDecryptCancelled},
		localdb.StateKeyMissing, apperr.KeyMissing); ok {
		e.emitChanged(m.ChatID, m.Seq)
	}
}

// assemble передаёт фрагменты сборке пачками из базы: курсор закрыт до обработки, внутри пачки
// assembleSlots горутин вызывают Add одновременно. Первая ошибка запоминается и останавливает пачку:
// остальные фрагменты не обрабатываются, горутины выходят без ожидания друг друга.
func (e *Engine) assemble(ctx context.Context, seq int64, asm *message.Assembler) error {
	after := -1
	for {
		list, err := e.cfg.Store.InFragments(seq, after, localdb.BatchRows)
		if err != nil {
			return storeError{err}
		}
		if len(list) == 0 {
			return nil
		}
		after = list[len(list)-1].Idx
		if err := assembleBatch(ctx, asm, list); err != nil {
			return err
		}
	}
}

// assembleBatch обрабатывает одну пачку и возвращает первую ошибку.
func assembleBatch(ctx context.Context, asm *message.Assembler, list []localdb.Fragment) error {
	bctx, stop := context.WithCancel(ctx)
	defer stop()
	var (
		mu    sync.Mutex
		first error
	)
	setErr := func(err error) {
		mu.Lock()
		if first == nil {
			first = err
		}
		mu.Unlock()
		stop()
	}
	jobs := make(chan localdb.Fragment)
	var wg sync.WaitGroup
	for i := 0; i < assembleSlots; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					setErr(apperr.New(apperr.Internal))
				}
			}()
			for f := range jobs {
				if bctx.Err() != nil {
					continue
				}
				frag := parse(f.Wire)
				if frag == nil {
					setErr(message.ErrFragmentMismatch)
					continue
				}
				if err := asm.Add(bctx, frag); err != nil {
					setErr(err)
				}
			}
		}()
	}
feed:
	for _, f := range list {
		select {
		case jobs <- f:
		case <-bctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if first == nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return first
}

// finish определяет вид содержимого и записывает результат.
func (e *Engine) finish(chat [16]byte, seq int64, meta message.Meta) error {
	st := e.cfg.Store
	var head []byte
	if meta.TotalSize > 0 {
		first, err := st.Chunks(seq, -1, 1)
		if err != nil {
			return storeError{err}
		}
		if len(first) == 1 {
			head = first[0].Data[:min(len(first[0].Data), 512)]
		}
	}
	c := Classify(meta, head, meta.TotalSize)
	typ := truncateUTF8(meta.Type, message.MaxTypeLen)
	switch c.Kind {
	case KindText, KindCancel:
		data, err := readAll(st, seq, meta.TotalSize)
		if err != nil {
			return err
		}
		kind := localdb.KindText
		if c.Kind == KindCancel {
			kind = localdb.KindControl
		}
		ok, err := st.FinishIncoming(seq, kind, string(data), "", "", "", meta.TotalSize)
		if err != nil {
			return storeError{err}
		}
		if !ok {
			return localdb.ErrStale
		}
		if c.Kind == KindCancel {
			e.peerCancelled(chat, data)
		}
		return nil
	case KindImage:
		ok, err := st.FinishIncoming(seq, localdb.KindImage, "", c.Name, typ, c.MediaType, meta.TotalSize)
		if err == nil && !ok {
			err = localdb.ErrStale
		}
		if err != nil && !errors.Is(err, localdb.ErrStale) {
			err = storeError{err}
		}
		return err
	default:
		ok, err := st.FinishIncoming(seq, localdb.KindFile, "", c.Name, typ, "", meta.TotalSize)
		if err == nil && !ok {
			err = localdb.ErrStale
		}
		if err != nil && !errors.Is(err, localdb.ErrStale) {
			err = storeError{err}
		}
		return err
	}
}

// readAll читает небольшое содержимое (текст, служебное) целиком, но не больше size байт.
func readAll(st Store, seq int64, size int64) ([]byte, error) {
	out := make([]byte, 0, size)
	after := -1
	for int64(len(out)) < size {
		list, err := st.Chunks(seq, after, localdb.BatchRows)
		if err != nil {
			return nil, storeError{err}
		}
		if len(list) == 0 {
			break
		}
		for _, c := range list {
			out = append(out, c.Data...)
		}
		after = list[len(list)-1].Idx
	}
	if int64(len(out)) > size {
		out = out[:size]
	}
	return out, nil
}

// peerCancelled обрабатывает служебное сообщение отмены от собеседника.
func (e *Engine) peerCancelled(chat [16]byte, data []byte) {
	if len(data) != 16 {
		e.log.Printf("служебное сообщение отмены неверной длины отброшено")
		return
	}
	var target [16]byte
	copy(target[:], data)
	seq, changed, err := e.cfg.Store.CancelledByPeer(chat, target)
	if err != nil {
		e.localWrite("отмена отправителем", err)
		return
	}
	if changed {
		e.cancelOp(seq)
		e.progress.Forget(seq)
		e.emitChanged(chat, seq)
	}
}
