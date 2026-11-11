package delivery

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/keyagree"
	"cryptochat/client/internal/localdb"
	"cryptochat/client/internal/rpcclient"
	"cryptochat/core/cipher"
	"cryptochat/core/message"
	"cryptochat/core/protocol"
)

// API — вызовы сервера, нужные доставке.
type API interface {
	SendFragment(ctx context.Context, chatID [16]byte, wire []byte) error
	OpenReceive(ctx context.Context, chatID [16]byte) (rpcclient.ReceiveStream, error)
}

// Store — хранилище профиля; реализация — *localdb.DB. Интерфейс позволяет тестам подменять отдельные
// операции, например имитировать ошибку записи.
type Store interface {
	Message(seq int64) (localdb.Message, bool, error)
	CreateOutgoing(m localdb.Message, firstChunk []byte) (int64, error)
	Transition(seq int64, from []int, to int, code string) (bool, error)
	SetWaiting(seq int64, code string) (bool, error)
	AddOutFragment(seq int64, idx int, wire []byte) (bool, error)
	OutFragment(seq int64, idx int) ([]byte, bool, error)
	FragmentSent(seq int64, idx int) (localdb.Message, bool, error)
	ReceiveBatch(chat [16]byte, in []localdb.Incoming, maxTotal uint32) ([]localdb.Received, error)
	InFragments(seq int64, after, limit int) ([]localdb.Fragment, error)
	Chunks(seq int64, after, limit int) ([]localdb.Chunk, error)
	PutChunk(seq int64, idx int, data []byte, complete bool) error
	ChunkWriterAt(seq int64) io.WriterAt
	FinishIncoming(seq int64, kind int, text, name, fileType, media string, size int64) (bool, error)
	CancelledByPeer(chat, id [16]byte) (int64, bool, error)
	ResetDecrypting() error
	PendingDecrypt() ([]int64, error)
	OutgoingChats() ([][16]byte, error)
	NextOutgoing(chat [16]byte) (localdb.Message, bool, error)
	Unfinished(chat [16]byte) ([]localdb.Message, error)
	SessionKey(id [16]byte) ([]byte, bool, error)
	Wanted(chat [16]byte) (wanted, ok bool, err error)
	SetWanted(chat [16]byte, wanted bool) error
}

// ChatInfo — то, что доставке нужно знать о чате. Mode и Padding — номера контракта (1–7, 1–4).
type ChatInfo struct {
	ID        [16]byte
	Algorithm cipher.Algorithm
	KeyBits   int
	Mode      int
	Padding   int
	KeyState  keyagree.State
}

// Events — получатель событий доставки (сервис). Обратные вызовы не должны ждать остановки доставки.
type Events interface {
	Messages(chat [16]byte, changed []MessageView)
	Progress(ProgressEvent)
	Notice(kind, code, message string)
	// ReceiveChanged — изменилось состояние приёма чата.
	ReceiveChanged(chat [16]byte)
	// ChatClosed — сервер ответил, что чат закрыт или не существует: нужна сверка чатов.
	ChatClosed(chat [16]byte)
	// SessionExpired — сервер не принял сессию.
	SessionExpired()
}

// Config — зависимости доставки.
type Config struct {
	API    API
	Store  Store
	Chat   func(id [16]byte) (ChatInfo, bool) // активный чат; false — чата нет среди активных
	Events Events
	Logger *log.Logger
	// Wait ждёт d или отмены ctx; false — отменено. Подменяется в тестах.
	Wait func(ctx context.Context, d time.Duration) bool
	Now  func() time.Time
}

// Состояния приёма чата.
const (
	RecvOff       = "OFF"
	RecvOpening   = "OPENING"
	RecvReceiving = "RECEIVING"
	RecvRetrying  = "RETRYING"
	RecvElsewhere = "ELSEWHERE"
)

// Паузы повторов: переоткрытие потока и временные отказы отправки; заполненная очередь получателя.
var (
	retryPauses = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second}
	queuePauses = []time.Duration{3 * time.Second, 6 * time.Second, 12 * time.Second, 15 * time.Second}
)

func pause(list []time.Duration, attempt int) time.Duration {
	if attempt >= len(list) {
		return list[len(list)-1]
	}
	return list[attempt]
}

// Ограничения параллельности: одновременных шифрований и расшифрований, горутин сборки на сообщение.
const (
	encryptSlots  = 2
	decryptSlots  = 2
	assembleSlots = 4
	batchWait     = 50 * time.Millisecond
	// encryptAhead — на сколько частей шифрование может опережать отправку: спул шифротекста в базе
	// не больше encryptAhead строк на сообщение.
	encryptAhead = 16
)

type recvState struct {
	state  string
	reason string
}

// Engine — доставка одной сессии. Создаётся после входа и останавливается до закрытия базы.
type Engine struct {
	cfg      Config
	log      *log.Logger
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	progress *Progress
	encSem   chan struct{}

	mu        sync.Mutex
	stopped   bool
	recv      map[[16]byte]recvState
	receivers map[[16]byte]*worker
	senders   map[[16]byte]*sender
	ops       map[int64]context.CancelFunc
	encoding  map[int64]bool
	queueNote map[int64]bool
	// maybeSent — у сообщения была отправка части с неизвестным исходом (обрыв, срок ответа, отмена во
	// время вызова): часть могла уйти к получателю.
	maybeSent map[int64]bool
	// writeFail — число подряд неудачных записей в базу при шифровании сообщения.
	writeFail map[int64]int

	dec *decryptor
}

// worker — фоновая задача чата, которую можно остановить и дождаться.
type worker struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// New создаёт доставку; Start запускает продолжение незавершённого.
func New(cfg Config) *Engine {
	if cfg.Logger == nil {
		cfg.Logger = log.New(os.Stderr, "cryptochat: ", log.LstdFlags)
	}
	if cfg.Wait == nil {
		cfg.Wait = func(ctx context.Context, d time.Duration) bool {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return false
			case <-t.C:
				return true
			}
		}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{cfg: cfg, log: cfg.Logger, ctx: ctx, cancel: cancel, encSem: make(chan struct{}, encryptSlots),
		recv: map[[16]byte]recvState{}, receivers: map[[16]byte]*worker{}, senders: map[[16]byte]*sender{},
		ops: map[int64]context.CancelFunc{}, encoding: map[int64]bool{}, queueNote: map[int64]bool{},
		maybeSent: map[int64]bool{}, writeFail: map[int64]int{}}
	e.progress = NewProgress(func(p ProgressEvent) { e.cfg.Events.Progress(p) }, cfg.Now)
	e.dec = newDecryptor(e)
	return e
}

// Start продолжает работу после перезапуска: прерванные расшифрования начинаются заново, незавершённые
// отправки продолжаются.
func (e *Engine) Start() {
	if err := e.cfg.Store.ResetDecrypting(); err != nil {
		e.log.Printf("доставка: возврат прерванных расшифрований: %v", err)
	}
	for i := 0; i < decryptSlots; i++ {
		e.goSafe("расшифрование", e.dec.run)
	}
	e.resume()
}

// resume запускает передатчики чатов с незавершёнными исходящими и ставит в очередь ожидающие
// расшифрования.
func (e *Engine) resume() {
	if chats, err := e.cfg.Store.OutgoingChats(); err == nil {
		for _, c := range chats {
			e.startSender(c)
		}
	}
	if seqs, err := e.cfg.Store.PendingDecrypt(); err == nil {
		for _, s := range seqs {
			e.dec.enqueue(s)
		}
	}
}

// Stop останавливает все потоки и операции без смены состояний сообщений и ждёт их завершения.
func (e *Engine) Stop() {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return
	}
	e.stopped = true
	e.mu.Unlock()
	e.cancel()
	e.dec.wake()
	e.wg.Wait()
}

// goSafe запускает фоновую задачу с перехватом паники: паника даёт уведомление INTERNAL, значение
// паники в журнал не пишется.
func (e *Engine) goSafe(where string, f func()) bool {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return false
	}
	e.wg.Add(1)
	e.mu.Unlock()
	go func() {
		defer e.wg.Done()
		e.protect(where, f)
	}()
	return true
}

func (e *Engine) protect(where string, f func()) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
			e.log.Printf("внутренняя ошибка доставки (%s)\n%s", where, debug.Stack())
			e.cfg.Events.Notice("error", apperr.Internal, "")
		}
	}()
	f()
	return true
}

func (e *Engine) wait(ctx context.Context, d time.Duration) bool { return e.cfg.Wait(ctx, d) }

// opContext создаёт контекст живой операции сообщения; CancelMessage отменяет его.
func (e *Engine) opContext(parent context.Context, seq int64) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	e.mu.Lock()
	e.ops[seq] = cancel
	e.mu.Unlock()
	return ctx, func() {
		e.mu.Lock()
		delete(e.ops, seq)
		e.mu.Unlock()
		cancel()
	}
}

func (e *Engine) cancelOp(seq int64) {
	e.mu.Lock()
	cancel := e.ops[seq]
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// emitChanged отправляет событие messages с текущими видами сообщений; скрытые не показываются.
func (e *Engine) emitChanged(chat [16]byte, seqs ...int64) {
	var out []MessageView
	seen := map[int64]bool{}
	for _, s := range seqs {
		if s == 0 || seen[s] {
			continue
		}
		seen[s] = true
		m, ok, err := e.cfg.Store.Message(s)
		if err != nil || !ok || hidden(m) {
			continue
		}
		out = append(out, e.View(m))
	}
	if len(out) > 0 {
		e.cfg.Events.Messages(chat, out)
	}
}

// hidden — сообщение не показывается в истории: служебное или строка-признак отмены, пришедшей раньше
// частей.
func hidden(m localdb.Message) bool {
	return m.Kind == localdb.KindControl ||
		m.Kind == localdb.KindPending && m.State == localdb.StateCancelledByPeer && m.Total == 0
}

func newMessageID() ([16]byte, error) {
	var id [16]byte
	_, err := io.ReadFull(rand.Reader, id[:])
	return id, err
}

// fragmentCount — число частей сообщения размером size.
func fragmentCount(size int64) int {
	if size <= 0 {
		return 1
	}
	return int((size-1)/message.ChunkSize + 1)
}

// storeError помечает ошибку записи в базу профиля среди ошибок шифрования и сборки.
type storeError struct{ err error }

func (s storeError) Error() string {
	return "запись в базу профиля: " + s.err.Error()
}
func (s storeError) Unwrap() error { return s.err }

func isStoreError(err error) bool {
	var s storeError
	return errors.As(err, &s)
}

// localWrite сообщает об ошибке записи в базу профиля.
func (e *Engine) localWrite(where string, err error) {
	e.log.Printf("доставка: %s: запись в базу профиля не удалась", where)
	_ = err
	e.cfg.Events.Notice("error", apperr.LocalWrite, "")
}

// params собирает параметры шифрования чата: реализация алгоритма из ciphers.Resolve, ключ из базы.
func params(chat ChatInfo, impl cipher.Algorithm, key []byte) message.Params {
	return message.Params{
		Settings: cipher.Settings{Algorithm: impl, KeyBits: chat.KeyBits,
			Mode: modeOf(chat.Mode), Padding: paddingOf(chat.Padding)},
		Key:    key,
		ChatID: chat.ID,
	}
}

// parse разбирает байты фрагмента; ошибка — nil.
func parse(wire []byte) *protocol.Fragment {
	f, err := protocol.Unmarshal(wire)
	if err != nil {
		return nil
	}
	return f
}
