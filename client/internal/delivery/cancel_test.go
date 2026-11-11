//go:build devcipher

package delivery

import (
	"bytes"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/localdb"
	"cryptochat/core/protocol"
)

// slowWriter замедляет запись кусков, чтобы расшифрование можно было застать в процессе.
type slowWriter struct {
	w     io.WriterAt
	delay time.Duration
}

func (s slowWriter) WriteAt(p []byte, off int64) (int, error) {
	time.Sleep(s.delay)
	return s.w.WriteAt(p, off)
}

func slowDecrypt(t *testing.T, d time.Duration) {
	wrapWriter = func(w io.WriterAt) io.WriterAt { return slowWriter{w, d} }
	t.Cleanup(func() { wrapWriter = func(w io.WriterAt) io.WriterAt { return w } })
}

// События прогресса всех четырёх стадий: первое сразу, не чаще 4 раз в секунду, последнее — done = total.
func TestProgressStages(t *testing.T) {
	slowDecrypt(t, 15*time.Millisecond)
	p := newPair(t, 6, 4, 128)
	p.a.receive()
	p.b.receive()
	data := randomBytes(60 * localdb.ChunkSize)
	start := time.Now()
	v := sendFile(t, p.a, p.id, data)
	got := p.b.waitIncoming(1, 30*time.Second, localdb.StateDone)
	// Первое событие стадии — не позже 1 с от начала операции; соседние промежуточные события — не
	// чаще 4 раз в секунду и не реже раза в секунду; последнее — done = total.
	check := func(ev *events, seq int64, stage Stage, since time.Time) {
		evs, times := ev.progressTimes(seq, stage)
		if len(evs) < 2 {
			t.Fatalf("%s: событий %d", stage, len(evs))
		}
		if d := times[0].Sub(since); d > time.Second {
			t.Errorf("%s: первое событие через %v", stage, d)
		}
		for i := 1; i < len(evs)-1; i++ {
			gap := times[i].Sub(times[i-1])
			if gap < progressInterval-10*time.Millisecond || gap > time.Second {
				t.Errorf("%s: промежуток между событиями %v", stage, gap)
			}
		}
		last := evs[len(evs)-1]
		if last.Done != last.Total || last.Total == 0 {
			t.Fatalf("%s: последнее %d/%d", stage, last.Done, last.Total)
		}
	}
	check(p.a.ev, v.Seq, StageEncrypt, start)
	check(p.a.ev, v.Seq, StageSend, start)
	check(p.b.ev, got[0].Seq, StageReceive, start)
	_, recvTimes := p.b.ev.progressTimes(got[0].Seq, StageReceive)
	check(p.b.ev, got[0].Seq, StageDecrypt, recvTimes[len(recvTimes)-1])
	empty := sendFile(t, p.a, p.id, nil)
	p.a.waitState(empty.Seq, 5*time.Second, localdb.StateSent)
	for _, e := range p.a.ev.progressOf(empty.Seq, StageEncrypt) {
		if e.Done != 0 || e.Total != 0 {
			t.Fatalf("пустой файл: %d/%d", e.Done, e.Total)
		}
	}
}

// Отмена отправителем во время передачи: CANCELLED не позже 2 с, спул и копия удалены, получатель
// получает служебную отмену; до первой отправленной части служебного сообщения нет.
func TestCancelOutgoing(t *testing.T) {
	p := newPair(t, 2, 3, 128)
	p.a.setFast()
	p.srv.SetQueueLimit(3) // передача встанет на третьей части
	p.a.receive()
	p.b.receive()
	p.srv.Pause(p.id, "bob", true)
	data := randomBytes(20 * localdb.ChunkSize)
	v := sendFile(t, p.a, p.id, data)
	p.a.waitState(v.Seq, 5*time.Second, localdb.StateWaiting)
	start := time.Now()
	cv, err := p.a.e.CancelMessage(v.Seq)
	if err != nil || cv.State != "CANCELLED" {
		t.Fatalf("%+v %v", cv, err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("отмена %v", d)
	}
	if w, ok, _ := p.a.db.OutFragment(v.Seq, 3); ok || w != nil {
		t.Fatal("спул не удалён")
	}
	if ch, _ := p.a.db.Chunks(v.Seq, -1, 1); len(ch) != 0 {
		t.Fatal("копия файла не удалена")
	}
	p.srv.SetQueueLimit(0)
	p.srv.Pause(p.id, "bob", false)
	got := p.b.waitIncoming(1, 10*time.Second, localdb.StateCancelledByPeer)
	if frs, _ := p.b.db.InFragments(got[0].Seq, -1, 16); len(frs) != 0 {
		t.Fatal("части отменённого остались у получателя")
	}
	if list, _, _ := p.b.db.ListMessages(p.id, 0, 50); len(list) != 1 {
		t.Fatalf("служебное сообщение видно в истории: %d", len(list))
	}
	if _, err := p.a.e.CancelMessage(v.Seq); apperr.CodeOf(err) != apperr.NotCancellable {
		t.Fatalf("повторная отмена: %v", err)
	}
	// До первой отправленной части служебного сообщения нет.
	p.srv.SetQueueLimit(1)
	p.srv.Pause(p.id, "bob", true)
	filler, _ := p.a.e.SendText(p.id, "занимает очередь")
	p.a.waitState(filler.Seq, 5*time.Second, localdb.StateSent)
	second, _ := p.a.e.SendText(p.id, "не уйдёт")
	p.a.waitState(second.Seq, 5*time.Second, localdb.StateWaiting)
	before := p.srv.Published()
	if _, err := p.a.e.CancelMessage(second.Seq); err != nil {
		t.Fatal(err)
	}
	p.srv.SetQueueLimit(0)
	time.Sleep(300 * time.Millisecond)
	if p.srv.Published() != before {
		t.Fatal("служебное сообщение ушло без отправленных частей")
	}
}

// Служебная отмена раньше частей: строка-признак (в истории не видна), части позже отбрасываются;
// повтор служебного — без изменений.
func TestCancelBeforeParts(t *testing.T) {
	p := newPair(t, 2, 3, 128)
	cap := &capture{}
	p.a.api.SetSendHook(func(n int, w []byte) (bool, error) { cap.hook(n, w); return false, nil })
	p.a.receive()
	p.b.receive()
	target := [16]byte{0x42}
	p.a.e.enqueueCancel(p.id, target)
	eventually(t, 5*time.Second, "служебное зашифровано", func() bool { cap.mu.Lock(); defer cap.mu.Unlock(); return len(cap.wires) == 1 })
	cap.mu.Lock()
	ctl := cap.wires[0]
	cap.mu.Unlock()
	p.srv.Inject(p.id, "bob", ctl)
	eventually(t, 5*time.Second, "признак", func() bool {
		m, ok, _ := p.b.db.MessageByID(p.id, localdb.DirIn, target)
		return ok && m.State == localdb.StateCancelledByPeer
	})
	late := &protocol.Fragment{ChatID: p.id, MessageID: target, Total: 2, IV: make([]byte, 16), Cipher: make([]byte, 16)}
	w, _ := late.Marshal()
	p.srv.Inject(p.id, "bob", w)
	p.srv.Inject(p.id, "bob", ctl) // повтор служебного
	eventually(t, 5*time.Second, "очередь пуста", func() bool { return p.srv.QueueLen(p.id, "bob") == 0 })
	m, _, _ := p.b.db.MessageByID(p.id, localdb.DirIn, target)
	if m.State != localdb.StateCancelledByPeer || m.ReceivedCount != 0 {
		t.Fatalf("признак изменился: %d %d", m.State, m.ReceivedCount)
	}
	// Строка-признак в истории не показывается.
	if list, _, _ := p.b.db.ListMessages(p.id, 0, 50); len(list) != 0 {
		t.Fatalf("в истории %d", len(list))
	}
}

// Отмена приёма: CANCELLED, части удалены, поток не закрыт, поздние части отбрасываются, следующее
// сообщение принимается.
func TestCancelReceiving(t *testing.T) {
	p := newPair(t, 2, 3, 128)
	cap := &capture{}
	p.a.api.SetSendHook(func(n int, w []byte) (bool, error) { cap.hook(n, w); return false, nil })
	p.a.receive()
	p.b.receive()
	data := randomBytes(5 * localdb.ChunkSize)
	v := sendFile(t, p.a, p.id, data)
	p.a.waitState(v.Seq, 5*time.Second, localdb.StateSent)
	frs := cap.frags(t)
	for _, f := range frs[:2] {
		w, _ := f.Marshal()
		p.srv.Inject(p.id, "bob", w)
	}
	got := p.b.waitIncoming(1, 5*time.Second, localdb.StateReceiving)
	eventually(t, 5*time.Second, "две части", func() bool { return p.b.msg(got[0].Seq).ReceivedCount == 2 })
	opens := p.b.api.Count("OpenReceive")
	cv, err := p.b.e.CancelMessage(got[0].Seq)
	if err != nil || cv.State != "CANCELLED" {
		t.Fatalf("%+v %v", cv, err)
	}
	if l, _ := p.b.db.InFragments(got[0].Seq, -1, 16); len(l) != 0 {
		t.Fatal("части не удалены")
	}
	for _, f := range frs[2:] {
		w, _ := f.Marshal()
		p.srv.Inject(p.id, "bob", w)
	}
	p.a.api.SetSendHook(nil)
	if _, err := p.a.e.SendText(p.id, "следующее"); err != nil {
		t.Fatal(err)
	}
	p.b.waitIncoming(1, 5*time.Second, localdb.StateDone)
	if p.b.msg(got[0].Seq).State != localdb.StateCancelled || p.b.api.Count("OpenReceive") != opens || !p.srv.Streaming(p.id, "bob") {
		t.Fatal("отмена приёма закрыла поток или изменилась")
	}
	eventually(t, 5*time.Second, "поздние части подтверждены", func() bool { return p.srv.QueueLen(p.id, "bob") == 0 })
}

// Отмена расшифрования: DECRYPT_CANCELLED, куски удалены, части целы, RetryDecrypt даёт DONE.
func TestCancelDecrypt(t *testing.T) {
	slowDecrypt(t, 40*time.Millisecond)
	p := newPair(t, 2, 3, 128)
	p.a.receive()
	p.b.receive()
	data := randomBytes(30 * localdb.ChunkSize)
	sendFile(t, p.a, p.id, data)
	got := p.b.waitIncoming(1, 20*time.Second, localdb.StateDecrypting)
	seq := got[0].Seq
	start := time.Now()
	cv, err := p.b.e.CancelMessage(seq)
	if err != nil || cv.State != "DECRYPT_CANCELLED" || !cv.Retryable {
		t.Fatalf("%+v %v", cv, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("отмена дольше 2 с")
	}
	time.Sleep(200 * time.Millisecond)
	if ch, _ := p.b.db.Chunks(seq, -1, 1); len(ch) != 0 {
		t.Fatal("частичные куски остались")
	}
	if l, _ := p.b.db.InFragments(seq, -1, 100); len(l) != 30 {
		t.Fatalf("частей %d", len(l))
	}
	if _, err := p.b.e.CancelMessage(seq); apperr.CodeOf(err) != apperr.NotCancellable {
		t.Fatalf("%v", err)
	}
	if _, err := p.b.e.RetryDecrypt(seq); err != nil {
		t.Fatal(err)
	}
	p.b.waitState(seq, 30*time.Second, localdb.StateDone)
	if !bytes.Equal(p.b.content(seq), data) {
		t.Fatal("содержимое")
	}
	if _, err := p.b.e.RetryDecrypt(seq); apperr.CodeOf(err) != apperr.NotRetryable {
		t.Fatalf("%v", err)
	}
	if _, err := p.b.e.CancelMessage(99999); apperr.CodeOf(err) != apperr.MessageNotFound {
		t.Fatalf("%v", err)
	}
}

// Отключение во время приёма: незаписанные части не подтверждены и приходят снова после подключения.
// Отмена одного из двух сообщений не останавливает второе.
func TestDisconnectAndParallelCancel(t *testing.T) {
	p := newPair(t, 2, 3, 128)
	var missed atomic.Int32
	var seenMu sync.Mutex
	seen := map[uint64]bool{}
	p.srv.OnAck(func(id [16]byte, login string, uid uint64) {
		if login != "bob" {
			return
		}
		// Считаются разные номера: подтверждение, прерванное закрытием потока, повторяется после выдачи снова.
		seenMu.Lock()
		seen[uid] = true
		n := int32(len(seen))
		seenMu.Unlock()
		stored := 0
		for _, m := range p.b.incoming() {
			stored += m.ReceivedCount
		}
		if int32(stored) < n {
			missed.Add(1)
		}
	})
	p.a.receive()
	p.b.receive()
	data := randomBytes(30 * localdb.ChunkSize)
	sendFile(t, p.a, p.id, data)
	eventually(t, 10*time.Second, "часть принята", func() bool {
		l := p.b.incoming()
		return len(l) == 1 && l[0].ReceivedCount > 0
	})
	if err := p.b.e.CloseReceiver(p.id); err != nil {
		t.Fatal(err)
	}
	if err := p.b.e.OpenReceiver(p.id); err != nil {
		t.Fatal(err)
	}
	got := p.b.waitIncoming(1, 20*time.Second, localdb.StateDone)
	if !bytes.Equal(p.b.content(got[0].Seq), data) || missed.Load() != 0 {
		t.Fatalf("целостность, подтверждений до записи: %d", missed.Load())
	}
	// Два сообщения одновременно: отмена одного.
	p.srv.SetQueueLimit(2)
	p.a.setFast()
	one := sendFile(t, p.a, p.id, randomBytes(10*localdb.ChunkSize))
	two, _ := p.a.e.SendText(p.id, "второе дойдёт")
	p.a.waitState(one.Seq, 10*time.Second, localdb.StateWaiting)
	if _, err := p.a.e.CancelMessage(one.Seq); err != nil {
		t.Fatal(err)
	}
	p.srv.SetQueueLimit(0)
	p.a.waitState(two.Seq, 10*time.Second, localdb.StateSent)
	eventually(t, 10*time.Second, "второе у bob", func() bool {
		for _, m := range p.b.incoming() {
			if m.Kind == localdb.KindText && m.Text == "второе дойдёт" {
				return true
			}
		}
		return false
	})
}
