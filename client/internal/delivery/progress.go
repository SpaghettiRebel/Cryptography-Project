package delivery

import (
	"sync"
	"time"
)

// Stage — стадия операции сообщения.
type Stage string

const (
	StageEncrypt Stage = "encrypt"
	StageSend    Stage = "send"
	StageReceive Stage = "receive"
	StageDecrypt Stage = "decrypt"
)

// progressInterval — наименьший промежуток между событиями одной стадии сообщения (не чаще 4 раз в
// секунду); первое и последнее значение стадии отправляются всегда.
const progressInterval = 250 * time.Millisecond

// ProgressEvent — событие прогресса для интерфейса. Done = Total = 0 — пустые данные.
type ProgressEvent struct {
	Seq    int64
	ChatID [16]byte
	Stage  Stage
	Done   int64
	Total  int64
}

type progressKey struct {
	seq   int64
	stage Stage
}

type progressState struct {
	done, total int64
	sentAt      time.Time
	sent        bool
	sentDone    int64
}

// Progress учитывает стадии живых операций и ограничивает частоту событий.
type Progress struct {
	now  func() time.Time
	emit func(ProgressEvent)

	mu    sync.Mutex
	state map[progressKey]*progressState
}

// NewProgress создаёт учёт; now нужен тестам, nil — системные часы.
func NewProgress(emit func(ProgressEvent), now func() time.Time) *Progress {
	if now == nil {
		now = time.Now
	}
	return &Progress{now: now, emit: emit, state: map[progressKey]*progressState{}}
}

// Update записывает значение стадии и отправляет событие, если это первое значение, последнее
// (done = total) или с прошлого события прошло не меньше progressInterval.
func (p *Progress) Update(seq int64, chat [16]byte, stage Stage, done, total int64) {
	k := progressKey{seq, stage}
	now := p.now()
	p.mu.Lock()
	st := p.state[k]
	if st == nil {
		st = &progressState{}
		p.state[k] = st
	}
	st.done, st.total = done, total
	send := !st.sent || (done >= total && st.sentDone != done) || now.Sub(st.sentAt) >= progressInterval
	if send {
		st.sent, st.sentAt, st.sentDone = true, now, done
	}
	p.mu.Unlock()
	if send && p.emit != nil {
		p.emit(ProgressEvent{Seq: seq, ChatID: chat, Stage: stage, Done: done, Total: total})
	}
}

// Get возвращает последнее значение стадии.
func (p *Progress) Get(seq int64, stage Stage) (done, total int64, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if st := p.state[progressKey{seq, stage}]; st != nil {
		return st.done, st.total, true
	}
	return 0, 0, false
}

// Forget удаляет значения всех стадий сообщения.
func (p *Progress) Forget(seq int64) {
	p.mu.Lock()
	for _, s := range []Stage{StageEncrypt, StageSend, StageReceive, StageDecrypt} {
		delete(p.state, progressKey{seq, s})
	}
	p.mu.Unlock()
}
