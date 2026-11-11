package delivery

import (
	"context"
	"errors"
	"io"
	"log"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/localdb"
	"cryptochat/core/message"
)

// badFragments — хранилище, в котором у сообщения одна пачка неразбираемых фрагментов.
type badFragments struct {
	Store
	n int
}

func (b badFragments) InFragments(_ int64, after, _ int) ([]localdb.Fragment, error) {
	if after >= 0 {
		return nil, nil
	}
	out := make([]localdb.Fragment, b.n)
	for i := range out {
		out[i] = localdb.Fragment{Idx: i, Wire: []byte{0xFF, byte(i)}}
	}
	return out, nil
}

// Все фрагменты полной пачки ошибочны: сборка завершается с ошибкой, а не зависает на отправке ошибок,
// которые никто не читает. Повторяется много раз на нескольких потоках: зависание зависело от порядка
// горутин.
func TestAssembleAllFragmentsFail(t *testing.T) {
	defer runtime.GOMAXPROCS(max(runtime.GOMAXPROCS(0), 4))
	e := New(Config{Store: badFragments{n: localdb.BatchRows}, Events: &events{}, Logger: log.New(io.Discard, "", 0)})
	for i := 0; i < 20000; i++ {
		done := make(chan error, 1)
		go func() { done <- e.assemble(context.Background(), 1, nil) }()
		select {
		case err := <-done:
			if !errors.Is(err, message.ErrFragmentMismatch) {
				t.Fatalf("проход %d: %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("проход %d: сборка зависла", i)
		}
	}
}

// Отменённый контекст до начала пачки: сборка сразу возвращает ошибку отмены.
func TestAssembleCancelled(t *testing.T) {
	e := New(Config{Store: badFragments{n: localdb.BatchRows}, Events: &events{}, Logger: log.New(io.Discard, "", 0)})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- e.assemble(ctx, 1, nil) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ошибки нет")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("сборка зависла")
	}
}

// «Получать здесь» сразу после события ELSEWHERE запускает новый приёмник: запись о прежнем удалена до
// события.
func TestReceiveHereRightAfterElsewhere(t *testing.T) {
	p := newPair(t, 2, 3, 128)
	p.b.receive()
	opens := p.b.api.Count("OpenReceive")
	var fired atomic.Bool
	p.b.ev.mu.Lock()
	p.b.ev.onRecv = func(chat [16]byte) {
		if st, _ := p.b.e.ReceiveState(chat); st == RecvElsewhere && fired.CompareAndSwap(false, true) {
			if err := p.b.e.OpenReceiver(chat); err != nil {
				t.Error(err)
			}
		}
	}
	p.b.ev.mu.Unlock()
	p.srv.EndStream(p.id, "bob", apperr.New(apperr.StreamReplaced))
	eventually(t, 5*time.Second, "приём снова открыт", func() bool {
		st, _ := p.b.e.ReceiveState(p.id)
		return fired.Load() && st == RecvReceiving && p.b.api.Count("OpenReceive") > opens
	})
}
