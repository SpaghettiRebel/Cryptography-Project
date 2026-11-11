//go:build devcipher

package delivery

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/ciphers"
	"cryptochat/client/internal/localdb"
	"cryptochat/core/cipher"
	"cryptochat/core/message"
	"cryptochat/core/protocol"
)

// Отмена расшифрования многочастного сообщения: расшифрование освобождает место, остановка доставки
// завершается в срок.
func TestCancelMultipartDecryptStops(t *testing.T) {
	slowDecrypt(t, 20*time.Millisecond)
	p := newPair(t, 2, 3, 128)
	p.a.receive()
	p.b.receive()
	sendFile(t, p.a, p.id, randomBytes(40*localdb.ChunkSize))
	got := p.b.waitIncoming(1, 20*time.Second, localdb.StateDecrypting)
	if _, err := p.b.e.CancelMessage(got[0].Seq); err != nil {
		t.Fatal(err)
	}
	// Освободившееся место расшифрования берёт следующее сообщение.
	if _, err := p.a.e.SendText(p.id, "после отмены"); err != nil {
		t.Fatal(err)
	}
	eventually(t, 10*time.Second, "следующее расшифровано", func() bool {
		for _, m := range p.b.incoming() {
			if m.State == localdb.StateDone && m.Text == "после отмены" {
				return true
			}
		}
		return false
	})
	stopped := make(chan struct{})
	go func() { p.b.e.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("остановка доставки зависла")
	}
}

// Сборка многочастного сообщения после отмены расшифрования: каждая часть возвращает ошибку отмены,
// сборка завершается в срок. Повторяется много раз на нескольких потоках.
func TestAssembleCancelledMultipart(t *testing.T) {
	defer runtime.GOMAXPROCS(max(runtime.GOMAXPROCS(0), 4))
	chat, id := [16]byte{1}, [16]byte{2}
	list := make([]localdb.Fragment, localdb.BatchRows)
	for i := range list {
		f := &protocol.Fragment{ChatID: chat, MessageID: id, Index: uint32(i), Total: 40, IV: make([]byte, 16), Cipher: make([]byte, 16)}
		w, err := f.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		list[i] = localdb.Fragment{Idx: i, Wire: w}
	}
	impl, err := ciphers.Resolve(cipher.RC6)
	if err != nil {
		t.Fatal(err)
	}
	info := ChatInfo{ID: chat, Algorithm: cipher.RC6, KeyBits: 128, Mode: 2, Padding: 3}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 20000; i++ {
		asm, err := message.NewAssembler(params(info, impl, make([]byte, 16)), id, discardWriter{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- assembleBatch(ctx, asm, list) }()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("проход %d: %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("проход %d: сборка зависла", i)
		}
	}
}

type discardWriter struct{}

func (discardWriter) WriteAt(p []byte, _ int64) (int, error) { return len(p), nil }

// RetryDecrypt сразу после отмены, пока прежний проход ещё не вышел, не теряется: сообщение
// расшифровывается заново.
func TestRetryRightAfterCancel(t *testing.T) {
	slowDecrypt(t, 40*time.Millisecond)
	p := newPair(t, 2, 3, 128)
	p.a.receive()
	p.b.receive()
	data := randomBytes(30 * localdb.ChunkSize)
	sendFile(t, p.a, p.id, data)
	got := p.b.waitIncoming(1, 20*time.Second, localdb.StateDecrypting)
	seq := got[0].Seq
	if _, err := p.b.e.CancelMessage(seq); err != nil {
		t.Fatal(err)
	}
	if _, err := p.b.e.RetryDecrypt(seq); err != nil {
		t.Fatal(err)
	}
	p.b.waitState(seq, 30*time.Second, localdb.StateDone)
	if !bytes.Equal(p.b.content(seq), data) {
		t.Fatal("содержимое")
	}
}

// Отмена исходящего, у которого ни одна часть не подтверждена, но первая могла уйти: получатель
// всё равно получает служебную отмену.
func TestCancelAfterUncertainSend(t *testing.T) {
	t.Run("ответ не получен, ждёт повтора", func(t *testing.T) {
		p := newPair(t, 2, 3, 128)
		var first atomic.Bool
		p.a.api.SetSendHook(func(int, []byte) (bool, error) {
			if first.CompareAndSwap(false, true) {
				return true, apperr.New(apperr.Timeout) // опубликовано, ответ потерян
			}
			return true, nil
		})
		p.a.receive()
		p.b.receive()
		v := sendFile(t, p.a, p.id, randomBytes(3*localdb.ChunkSize))
		m := p.a.waitState(v.Seq, 5*time.Second, localdb.StateWaiting)
		if m.SentCount != 0 || m.ErrorCode != apperr.Timeout {
			t.Fatalf("%+v", m)
		}
		if _, err := p.a.e.CancelMessage(v.Seq); err != nil {
			t.Fatal(err)
		}
		p.b.waitIncoming(1, 10*time.Second, localdb.StateCancelledByPeer)
	})
	t.Run("отмена во время отправки", func(t *testing.T) {
		p := newPair(t, 2, 3, 128)
		entered, release := make(chan struct{}), make(chan struct{})
		var first atomic.Bool
		p.a.api.SetSendHook(func(int, []byte) (bool, error) {
			if first.CompareAndSwap(false, true) {
				close(entered)
				<-release
			}
			return true, nil
		})
		p.a.receive()
		p.b.receive()
		v := sendFile(t, p.a, p.id, randomBytes(3*localdb.ChunkSize))
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("отправка не началась")
		}
		if _, err := p.a.e.CancelMessage(v.Seq); err != nil {
			close(release)
			t.Fatal(err)
		}
		close(release)
		p.b.waitIncoming(1, 10*time.Second, localdb.StateCancelledByPeer)
	})
}

// При медленной отправке шифрование не уходит вперёд больше чем на encryptAhead частей: спул
// шифротекста в базе не растёт с размером файла.
func TestEncryptAheadLimit(t *testing.T) {
	p := newPair(t, 2, 3, 128)
	p.a.api.SetSendHook(func(int, []byte) (bool, error) {
		time.Sleep(15 * time.Millisecond)
		return true, nil
	})
	p.a.receive()
	p.b.receive()
	v := sendFile(t, p.a, p.id, randomBytes(80*localdb.ChunkSize))
	most := 0
	for {
		m := p.a.msg(v.Seq)
		if d := m.EncryptedCount - m.SentCount; d > most {
			most = d
		}
		if m.State == localdb.StateSent {
			break
		}
		if localdb.Final(m.State) {
			t.Fatalf("состояние %d", m.State)
		}
		time.Sleep(3 * time.Millisecond)
	}
	if most > encryptAhead+localdb.BatchRows {
		t.Fatalf("в спуле до %d строк, предел %d", most, encryptAhead+localdb.BatchRows)
	}
	if most > encryptAhead {
		t.Fatalf("шифрование опередило отправку на %d частей", most)
	}
	p.b.waitIncoming(1, 30*time.Second, localdb.StateDone)
}

// Повторяющийся сбой записи при шифровании: одно уведомление LOCAL_WRITE на сообщение, пауза растёт,
// после восстановления сообщение уходит.
func TestEncryptWriteFailureNotice(t *testing.T) {
	p := newPair(t, 2, 3, 128)
	f := p.a.withFaults()
	p.a.setFast()
	f.mu.Lock()
	f.failAdd = 4
	f.mu.Unlock()
	p.a.receive()
	p.b.receive()
	v, err := p.a.e.SendText(p.id, "после сбоев записи")
	if err != nil {
		t.Fatal(err)
	}
	p.a.waitState(v.Seq, 30*time.Second, localdb.StateSent)
	if n := p.a.ev.noticeCount(apperr.LocalWrite); n != 1 {
		t.Fatalf("уведомлений LOCAL_WRITE: %d", n)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second}
	waits, i := p.a.waitsCopy(), 0
	for _, d := range waits {
		if i < len(want) && d == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("паузы %v, ожидались по порядку %v", waits, want)
	}
	p.b.waitIncoming(1, 10*time.Second, localdb.StateDone)
}

// Неверные параметры чата у получателя — не повреждение: сообщение ждёт с причиной, части целы,
// после исправления повтор расшифровывает его.
func TestParamsErrorKeepsParts(t *testing.T) {
	p := newPair(t, 2, 3, 128)
	p.b.mu.Lock()
	p.b.chat.Mode = 99
	p.b.mu.Unlock()
	p.a.receive()
	p.b.receive()
	if _, err := p.a.e.SendText(p.id, "параметры"); err != nil {
		t.Fatal(err)
	}
	var m localdb.Message
	eventually(t, 10*time.Second, "ожидание с причиной", func() bool {
		l := p.b.incoming()
		if len(l) == 1 {
			m = l[0]
		}
		return len(l) == 1 && m.ErrorCode != ""
	})
	if m.State != localdb.StateDecryptPending || m.ErrorCode != apperr.InvalidChatParams {
		t.Fatalf("состояние %d, код %q", m.State, m.ErrorCode)
	}
	if l, _ := p.b.db.InFragments(m.Seq, -1, 16); len(l) != 1 {
		t.Fatalf("частей %d", len(l))
	}
	if v := p.b.e.View(m); !v.Retryable || !v.Cancellable {
		t.Fatalf("%+v", v)
	}
	p.b.mu.Lock()
	p.b.chat.Mode = 2
	p.b.mu.Unlock()
	if _, err := p.b.e.RetryDecrypt(m.Seq); err != nil {
		t.Fatal(err)
	}
	if got := p.b.waitState(m.Seq, 10*time.Second, localdb.StateDone); got.Text != "параметры" {
		t.Fatalf("%q", got.Text)
	}
}
