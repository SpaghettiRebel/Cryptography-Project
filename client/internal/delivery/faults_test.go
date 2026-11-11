//go:build devcipher

package delivery

import (
	"bytes"
	"math/rand/v2"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/keyagree"
	"cryptochat/client/internal/localdb"
	"cryptochat/core/protocol"
)

// Части в обратном и случайном порядке и каждая дважды (в том числе повторная выдача после обрыва) —
// одно готовое сообщение с верным содержимым (FR-011).
func TestOrderAndDuplicates(t *testing.T) {
	for _, order := range []string{"обратный", "случайный"} {
		t.Run(order, func(t *testing.T) {
			p := newPair(t, 3, 2, 128)
			p.b.setFast()
			p.srv.SetDuplicate(true)
			p.a.receive()
			p.b.receive()
			p.srv.Pause(p.id, "bob", true)
			data := randomBytes(6*localdb.ChunkSize + 3)
			v := sendFile(t, p.a, p.id, data)
			p.a.waitState(v.Seq, 10*time.Second, localdb.StateSent)
			if order == "обратный" {
				p.srv.SetPicker(func(n int) int { return n - 1 })
			} else {
				p.srv.SetPicker(func(n int) int { return rand.IntN(n) })
			}
			// Первая выдача обрывается: невыданное и неподтверждённое придёт снова с признаком повтора.
			p.srv.Pause(p.id, "bob", false)
			time.Sleep(20 * time.Millisecond)
			p.srv.EndStream(p.id, "bob", apperr.New(apperr.ServerUnavailable))
			got := p.b.waitIncoming(1, 10*time.Second, localdb.StateDone)
			if len(got) != 1 || !bytes.Equal(p.b.content(got[0].Seq), data) {
				t.Fatal("содержимое не совпало")
			}
			eventually(t, 5*time.Second, "очередь пуста", func() bool { return p.srv.QueueLen(p.id, "bob") == 0 })
			if n := len(p.b.incoming()); n != 1 {
				t.Fatalf("входящих %d", n)
			}
		})
	}
}

// Повтор сообщения после готовности, мусор, чужой чат, слишком много частей и несовпадение числа
// частей подтверждаются и отбрасываются; приём продолжается.
func TestBadFragments(t *testing.T) {
	p := newPair(t, 2, 3, 128)
	cap := &capture{}
	p.a.api.SetSendHook(cap.hook)
	p.a.receive()
	p.b.receive()
	if _, err := p.a.e.SendText(p.id, "первое"); err != nil {
		t.Fatal(err)
	}
	p.b.waitIncoming(1, 5*time.Second, localdb.StateDone)
	for _, f := range cap.frags(t) {
		w, _ := f.Marshal()
		p.srv.Inject(p.id, "bob", w) // повтор всего сообщения
	}
	p.srv.Inject(p.id, "bob", []byte("не фрагмент"))
	foreign := &protocol.Fragment{ChatID: [16]byte{0xAB}, MessageID: [16]byte{1}, Total: 1, IV: make([]byte, 16), Cipher: make([]byte, 16)}
	w, _ := foreign.Marshal()
	p.srv.Inject(p.id, "bob", w)
	huge := &protocol.Fragment{ChatID: p.id, MessageID: [16]byte{2}, Total: MaxTotal + 1, IV: make([]byte, 16), Cipher: make([]byte, 16)}
	w, _ = huge.Marshal()
	p.srv.Inject(p.id, "bob", w)
	first := &protocol.Fragment{ChatID: p.id, MessageID: [16]byte{3}, Total: 3, IV: make([]byte, 16), Cipher: make([]byte, 16)}
	w, _ = first.Marshal()
	p.srv.Inject(p.id, "bob", w)
	eventually(t, 5*time.Second, "первая часть записана", func() bool { return len(p.b.incoming()) == 2 })
	other := &protocol.Fragment{ChatID: p.id, MessageID: [16]byte{3}, Index: 1, Total: 4, IV: make([]byte, 16), Cipher: make([]byte, 16)}
	w, _ = other.Marshal()
	p.srv.Inject(p.id, "bob", w)
	if _, err := p.a.e.SendText(p.id, "второе"); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "второе принято", func() bool {
		n := 0
		for _, m := range p.b.incoming() {
			if m.State == localdb.StateDone {
				n++
			}
		}
		return n == 2
	})
	eventually(t, 5*time.Second, "очередь пуста", func() bool { return p.srv.QueueLen(p.id, "bob") == 0 })
	var corrupted int
	for _, m := range p.b.incoming() {
		if m.State == localdb.StateCorrupted && m.ErrorCode == apperr.MessageCorrupted {
			corrupted++
		}
	}
	if corrupted != 1 || len(p.b.incoming()) != 3 {
		t.Fatalf("повреждённых %d, всего %d", corrupted, len(p.b.incoming()))
	}
}

// Подменённый байт шифротекста при PKCS7 — CORRUPTED.
func TestTamperedCiphertext(t *testing.T) {
	p := newPair(t, 2, 3, 128)
	p.a.api.SetSendHook(func(_ int, wire []byte) (bool, error) {
		f, _ := protocol.Unmarshal(wire)
		f.Cipher[len(f.Cipher)-1] ^= 0xFF
		w, _ := f.Marshal()
		p.srv.Inject(p.id, "bob", w)
		return false, nil
	})
	p.a.receive()
	p.b.receive()
	if _, err := p.a.e.SendText(p.id, "подмена"); err != nil {
		t.Fatal(err)
	}
	got := p.b.waitIncoming(1, 5*time.Second, localdb.StateCorrupted)
	if got[0].ErrorCode != apperr.MessageCorrupted {
		t.Fatal(got[0].ErrorCode)
	}
}

// Ошибка записи в базу: подтверждения нет, уведомление LOCAL_WRITE, поток переоткрывается, части
// приходят снова. Паника обработки даёт INTERNAL, приём продолжается.
func TestStoreFailureAndPanic(t *testing.T) {
	p := newPair(t, 2, 3, 128)
	p.b.setFast()
	f := p.b.withFaults()
	f.mu.Lock()
	f.failBatch = 1
	f.mu.Unlock()
	p.a.receive()
	p.b.receive()
	opens := p.b.api.Count("OpenReceive")
	if _, err := p.a.e.SendText(p.id, "после сбоя"); err != nil {
		t.Fatal(err)
	}
	got := p.b.waitIncoming(1, 10*time.Second, localdb.StateDone)
	if got[0].Text != "после сбоя" || p.b.ev.noticeCount(apperr.LocalWrite) != 1 || p.b.api.Count("OpenReceive") <= opens {
		t.Fatalf("уведомлений %d, открытий %d→%d", p.b.ev.noticeCount(apperr.LocalWrite), opens, p.b.api.Count("OpenReceive"))
	}
	if len(p.srv.Acks()) == 0 {
		t.Fatal("нет подтверждений")
	}
	f.mu.Lock()
	f.panicBatch = 1
	f.mu.Unlock()
	if _, err := p.a.e.SendText(p.id, "после паники"); err != nil {
		t.Fatal(err)
	}
	p.b.waitIncoming(2, 10*time.Second, localdb.StateDone)
	if p.b.ev.noticeCount(apperr.Internal) != 1 {
		t.Fatal("нет уведомления INTERNAL")
	}
}

// Каждая причина завершения потока даёт своё действие.
func TestStreamEndings(t *testing.T) {
	retry := []string{apperr.ServerBusy, apperr.ServerUnavailable, apperr.AckTimeout, apperr.Internal}
	for _, code := range retry {
		t.Run(code, func(t *testing.T) {
			p := newPair(t, 2, 3, 128)
			p.b.setFast()
			p.b.receive()
			n := p.b.api.Count("OpenReceive")
			p.srv.EndStream(p.id, "bob", apperr.New(code))
			eventually(t, 3*time.Second, "переоткрытие", func() bool {
				st, _ := p.b.e.ReceiveState(p.id)
				return p.b.api.Count("OpenReceive") > n && st == RecvReceiving
			})
			for _, d := range p.b.waitsCopy() {
				if d > 5*time.Second {
					t.Fatalf("пауза %v", d)
				}
			}
		})
	}
	t.Run("NOT_CONNECTED", func(t *testing.T) {
		p := newPair(t, 2, 3, 128)
		p.b.receive()
		p.srv.EndStream(p.id, "bob", apperr.New(apperr.NotConnected))
		eventually(t, 3*time.Second, "OFF", func() bool { st, _ := p.b.e.ReceiveState(p.id); return st == RecvOff })
		if w, ok, _ := p.b.db.Wanted(p.id); w || !ok {
			t.Fatal("желание принимать не снято")
		}
		p.b.e.Sync(map[[16]byte]bool{p.id: true})
		time.Sleep(100 * time.Millisecond)
		if st, _ := p.b.e.ReceiveState(p.id); st != RecvOff {
			t.Fatal("поток открылся снова")
		}
	})
	t.Run("CHAT_CLOSED", func(t *testing.T) {
		p := newPair(t, 2, 3, 128)
		p.b.receive()
		p.srv.EndStream(p.id, "bob", apperr.New(apperr.ChatClosed))
		eventually(t, 3*time.Second, "обработка закрытия", func() bool { p.b.ev.mu.Lock(); defer p.b.ev.mu.Unlock(); return p.b.ev.closed == 1 })
	})
	t.Run("UNAUTHENTICATED", func(t *testing.T) {
		p := newPair(t, 2, 3, 128)
		p.b.receive()
		p.srv.EndStream(p.id, "bob", apperr.New(apperr.SessionExpired))
		eventually(t, 3*time.Second, "конец сессии", func() bool { p.b.ev.mu.Lock(); defer p.b.ev.mu.Unlock(); return p.b.ev.expired == 1 })
	})
	t.Run("STREAM_REPLACED", func(t *testing.T) {
		p := newPair(t, 2, 3, 128)
		p.b.setFast()
		p.b.receive()
		n := p.b.api.Count("OpenReceive")
		p.srv.EndStream(p.id, "bob", apperr.New(apperr.StreamReplaced))
		eventually(t, 3*time.Second, "ELSEWHERE", func() bool { st, _ := p.b.e.ReceiveState(p.id); return st == RecvElsewhere })
		p.b.e.Sync(map[[16]byte]bool{p.id: true})
		time.Sleep(time.Second)
		if p.b.api.Count("OpenReceive") != n {
			t.Fatal("поток переоткрыт сам после замены")
		}
		if p.b.ev.noticeCount(apperr.StreamReplaced) != 1 {
			t.Fatal("нет уведомления")
		}
		if err := p.b.e.OpenReceiver(p.id); err != nil {
			t.Fatal(err)
		}
		eventually(t, 3*time.Second, "приём здесь", func() bool { st, _ := p.b.e.ReceiveState(p.id); return st == RecvReceiving })
	})
}

// Заполненная очередь: WAITING/QUEUE_FULL, повтор той же части с паузами 3, 6, 12, 15 с, продолжение после
// освобождения, ни одна часть не принята дважды.
func TestQueueFull(t *testing.T) {
	p := newPair(t, 6, 4, 128)
	p.a.setFast()
	p.srv.SetQueueLimit(3)
	p.a.receive()
	data := randomBytes(8 * localdb.ChunkSize)
	v := sendFile(t, p.a, p.id, data)
	m := p.a.waitState(v.Seq, 5*time.Second, localdb.StateWaiting)
	if m.ErrorCode != apperr.QueueFull || m.SentCount != 3 {
		t.Fatalf("%q %d", m.ErrorCode, m.SentCount)
	}
	eventually(t, 3*time.Second, "четыре паузы", func() bool { return len(p.a.waitsCopy()) >= 5 })
	w := p.a.waitsCopy()
	want := []time.Duration{3 * time.Second, 6 * time.Second, 12 * time.Second, 15 * time.Second, 15 * time.Second}
	for i := range want {
		if w[i] != want[i] {
			t.Fatalf("паузы %v", w)
		}
	}
	if p.a.ev.noticeCount(apperr.QueueFull) != 1 {
		t.Fatal("уведомление QUEUE_FULL не одно")
	}
	p.b.receive()
	p.srv.SetQueueLimit(0)
	got := p.b.waitIncoming(1, 10*time.Second, localdb.StateDone)
	if !bytes.Equal(p.b.content(got[0].Seq), data) {
		t.Fatal("содержимое")
	}
	if n := p.srv.Published(); n != 8 {
		t.Fatalf("принято публикаций %d из 8", n)
	}
	if m := p.a.msg(v.Seq); m.State != localdb.StateSent || m.ErrorCode != "" {
		t.Fatalf("%d %q", m.State, m.ErrorCode)
	}
}

// Ответы SendFragment: потерянный ответ, временные отказы, NOT_CONNECTED, CHAT_CLOSED, ошибка формата,
// окончание сессии.
func TestSendErrors(t *testing.T) {
	script := func(p *pair, errs map[int]error, publish map[int]bool) {
		p.a.api.SetSendHook(func(n int, _ []byte) (bool, error) {
			pub, ok := publish[n]
			if !ok {
				pub = errs[n] == nil
			}
			return pub, errs[n]
		})
	}
	t.Run("потерянный ответ", func(t *testing.T) {
		p := newPair(t, 2, 3, 128)
		p.a.setFast()
		script(p, map[int]error{2: apperr.New(apperr.Timeout)}, map[int]bool{2: true})
		p.a.receive()
		p.b.receive()
		data := randomBytes(3 * localdb.ChunkSize)
		v := sendFile(t, p.a, p.id, data)
		p.a.waitState(v.Seq, 5*time.Second, localdb.StateSent)
		got := p.b.waitIncoming(1, 5*time.Second, localdb.StateDone)
		if len(p.b.incoming()) != 1 || !bytes.Equal(p.b.content(got[0].Seq), data) {
			t.Fatal("одно целое сообщение")
		}
	})
	t.Run("временные отказы", func(t *testing.T) {
		p := newPair(t, 2, 3, 128)
		p.a.setFast()
		script(p, map[int]error{1: apperr.New(apperr.ChatNotReady), 2: apperr.New(apperr.ServerBusy), 3: apperr.New(apperr.NotConnected)}, nil)
		p.a.receive()
		p.b.receive()
		v, _ := p.a.e.SendText(p.id, "дойдёт")
		p.a.waitState(v.Seq, 15*time.Second, localdb.StateSent)
		p.b.waitIncoming(1, 5*time.Second, localdb.StateDone)
	})
	t.Run("CHAT_CLOSED", func(t *testing.T) {
		p := newPair(t, 2, 3, 128)
		script(p, map[int]error{1: apperr.New(apperr.ChatClosed)}, nil)
		p.a.receive()
		v, _ := p.a.e.SendText(p.id, "x")
		p.a.waitState(v.Seq, 5*time.Second, localdb.StateUndelivered)
	})
	t.Run("ошибка формата", func(t *testing.T) {
		p := newPair(t, 2, 3, 128)
		script(p, map[int]error{1: apperr.New(apperr.Internal)}, nil)
		p.a.receive()
		v, _ := p.a.e.SendText(p.id, "x")
		m := p.a.waitState(v.Seq, 5*time.Second, localdb.StateFailed)
		if m.ErrorCode != apperr.Internal {
			t.Fatal(m.ErrorCode)
		}
	})
	t.Run("UNAUTHENTICATED", func(t *testing.T) {
		p := newPair(t, 2, 3, 128)
		script(p, map[int]error{1: apperr.New(apperr.SessionExpired)}, nil)
		p.a.receive()
		p.b.receive()
		v, _ := p.a.e.SendText(p.id, "после входа")
		eventually(t, 3*time.Second, "конец сессии", func() bool { p.a.ev.mu.Lock(); defer p.a.ev.mu.Unlock(); return p.a.ev.expired > 0 })
		if m := p.a.msg(v.Seq); localdb.Final(m.State) {
			t.Fatal("состояние изменилось")
		}
		p.a.restart() // новый вход
		p.a.receive()
		p.a.waitState(v.Seq, 10*time.Second, localdb.StateSent)
		p.b.waitIncoming(1, 5*time.Second, localdb.StateDone)
	})
}

// Перезапуск отправителя посреди шифрования и посреди передачи: продолжение без повторной отправки
// отправленного, у получателя одно целое сообщение.
func TestSenderRestart(t *testing.T) {
	p := newPair(t, 2, 3, 128)
	p.a.setFast()
	var allowed atomic.Int32
	allowed.Store(2)
	var mu sync.Mutex
	sentIdx := map[uint32]int{}
	p.a.api.SetSendHook(func(_ int, w []byte) (bool, error) {
		if allowed.Add(-1) < 0 {
			return false, apperr.New(apperr.ServerUnavailable)
		}
		f, _ := protocol.Unmarshal(w)
		mu.Lock()
		sentIdx[f.Index]++
		mu.Unlock()
		return true, nil
	})
	p.a.receive()
	p.b.receive()
	data := randomBytes(12 * localdb.ChunkSize)
	v := sendFile(t, p.a, p.id, data)
	eventually(t, 5*time.Second, "две части отправлены", func() bool { return p.a.msg(v.Seq).SentCount == 2 })
	p.a.restart()
	p.a.receive()
	allowed.Store(3)
	eventually(t, 5*time.Second, "ещё части", func() bool { return p.a.msg(v.Seq).SentCount == 5 })
	p.a.restart()
	allowed.Store(1000)
	p.a.receive()
	p.a.waitState(v.Seq, 10*time.Second, localdb.StateSent)
	got := p.b.waitIncoming(1, 10*time.Second, localdb.StateDone)
	if !bytes.Equal(p.b.content(got[0].Seq), data) || len(p.b.incoming()) != 1 {
		t.Fatal("содержимое")
	}
	mu.Lock()
	defer mu.Unlock()
	for i, n := range sentIdx {
		if n != 1 {
			t.Fatalf("часть %d отправлена %d раз", i, n)
		}
	}
}

// Источник изменён до полной копии (продолжение после перезапуска) и во время шифрования (без
// перезапуска, со служебной отменой).
func TestSourceChanged(t *testing.T) {
	t.Run("после перезапуска", func(t *testing.T) {
		p := newPair(t, 2, 3, 128)
		data := randomBytes(3 * localdb.ChunkSize)
		path := writeFile(t, data)
		st, _ := os.Stat(path)
		p.a.close()
		db, err := localdb.Open(p.a.path)
		if err != nil {
			t.Fatal(err)
		}
		seq, err := db.CreateOutgoing(localdb.Message{ChatID: p.id, MessageID: [16]byte{7}, Kind: localdb.KindFile, FileName: "f",
			FileType: "x", Size: st.Size(), Total: 3, SourcePath: path, SourceSize: st.Size(), SourceMtime: st.ModTime().UnixNano()}, nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
		if err := os.WriteFile(path, data[:100], 0o600); err != nil {
			t.Fatal(err)
		}
		p.a.open()
		m := p.a.waitState(seq, 5*time.Second, localdb.StateFailed)
		if m.ErrorCode != apperr.SourceChanged {
			t.Fatal(m.ErrorCode)
		}
	})
	t.Run("во время шифрования", func(t *testing.T) {
		p := newPair(t, 2, 3, 128)
		data := randomBytes(150 * localdb.ChunkSize)
		path := writeFile(t, data)
		st, _ := os.Stat(path)
		var once sync.Once
		p.a.api.SetSendHook(func(n int, _ []byte) (bool, error) {
			once.Do(func() {
				if err := os.Truncate(path, 1000); err != nil {
					t.Error(err)
				}
			})
			return true, nil
		})
		p.a.receive()
		p.b.receive()
		v, err := p.a.e.SendFile(p.id, path, st.Size(), st.ModTime())
		if err != nil {
			t.Fatal(err)
		}
		m := p.a.waitState(v.Seq, 20*time.Second, localdb.StateFailed)
		if m.ErrorCode != apperr.SourceChanged || m.SentCount == 0 {
			t.Fatalf("%q, отправлено %d", m.ErrorCode, m.SentCount)
		}
		got := p.b.waitIncoming(1, 10*time.Second, localdb.StateCancelledByPeer)
		if frs, _ := p.b.db.InFragments(got[0].Seq, -1, 16); len(frs) != 0 {
			t.Fatal("части отменённого остались")
		}
		if ch, _ := p.a.db.Chunks(v.Seq, -1, 1); len(ch) != 0 {
			t.Fatal("копия файла осталась")
		}
	})
}

// Обрыв после записи части и до подтверждения: часть приходит снова и не дублируется. Перезапуск во
// время расшифрования возвращает сообщение в очередь расшифрования.
func TestReceiverRestart(t *testing.T) {
	p := newPair(t, 2, 3, 128)
	p.b.setFast()
	var once sync.Once
	p.srv.OnAck(func(id [16]byte, login string, uid uint64) {
		if login == "bob" {
			once.Do(func() { p.srv.EndStream(p.id, "bob", apperr.New(apperr.ServerUnavailable)) })
		}
	})
	p.a.receive()
	p.b.receive()
	data := randomBytes(4 * localdb.ChunkSize)
	sendFile(t, p.a, p.id, data)
	got := p.b.waitIncoming(1, 10*time.Second, localdb.StateDone)
	if !bytes.Equal(p.b.content(got[0].Seq), data) || got[0].ReceivedCount != 4 {
		t.Fatalf("принято частей %d", got[0].ReceivedCount)
	}
	// Перезапуск во время расшифрования.
	p.b.mu.Lock()
	p.b.key = keyagree.WaitingPeer
	p.b.mu.Unlock()
	v, _ := p.a.e.SendText(p.id, "второе")
	_ = v
	list := p.b.waitIncoming(1, 5*time.Second, localdb.StateDecryptPending)
	var seq int64
	for _, m := range list {
		if m.State == localdb.StateDecryptPending {
			seq = m.Seq
		}
	}
	p.b.close()
	db, _ := localdb.Open(p.b.path)
	_, _ = db.Transition(seq, []int{localdb.StateDecryptPending}, localdb.StateDecrypting, "")
	_ = db.Close()
	p.b.mu.Lock()
	p.b.key = keyagree.Ready
	p.b.mu.Unlock()
	p.b.open()
	if m := p.b.waitState(seq, 5*time.Second, localdb.StateDone); m.Text != "второе" {
		t.Fatal(m.Text)
	}
}

// Расшифрование большого сообщения идёт одновременно с приёмом в другом чате (одно соединение с базой).
func TestDecryptAlongsideReceive(t *testing.T) {
	p := newPair(t, 2, 3, 128)
	other := p.addChat(t)
	p.a.receive()
	p.b.receive()
	if err := p.b.e.OpenReceiver(other); err != nil {
		t.Fatal(err)
	}
	if err := p.a.e.OpenReceiver(other); err != nil {
		t.Fatal(err)
	}
	data := randomBytes(40 * localdb.ChunkSize)
	sendFile(t, p.a, p.id, data)
	deadline := time.Now().Add(30 * time.Second)
	texts := 0
	for {
		if _, err := p.a.e.SendText(other, "параллельно"); err != nil {
			t.Fatal(err)
		}
		texts++
		done := false
		for _, m := range p.b.incoming() {
			if m.State == localdb.StateDone {
				done = true
			}
		}
		if done || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := p.b.waitIncoming(1, 30*time.Second, localdb.StateDone)
	if !bytes.Equal(p.b.content(got[0].Seq), data) {
		t.Fatal("содержимое")
	}
	eventually(t, 30*time.Second, "тексты другого чата", func() bool {
		list, _, _ := p.b.db.ListMessages(other, 0, 200)
		n := 0
		for _, m := range list {
			if m.State == localdb.StateDone {
				n++
			}
		}
		return n == texts
	})
}

// Закрытие чата: принимаемое — UNDELIVERED, ожидающее расшифрования — KEY_MISSING, исходящее — UNDELIVERED.
func TestChatClosed(t *testing.T) {
	p := newPair(t, 2, 3, 128)
	p.b.mu.Lock()
	p.b.key = keyagree.WaitingPeer
	p.b.mu.Unlock()
	p.a.receive()
	p.b.receive()
	v, _ := p.a.e.SendText(p.id, "ждёт ключа")
	p.a.waitState(v.Seq, 5*time.Second, localdb.StateSent)
	pending := p.b.waitIncoming(1, 5*time.Second, localdb.StateDecryptPending)
	part := &protocol.Fragment{ChatID: p.id, MessageID: [16]byte{5}, Total: 2, IV: make([]byte, 16), Cipher: make([]byte, 16)}
	w, _ := part.Marshal()
	p.srv.Inject(p.id, "bob", w)
	eventually(t, 5*time.Second, "принимается", func() bool { return len(p.b.incoming()) == 2 })
	p.a.api.SetSendHook(func(int, []byte) (bool, error) { return false, apperr.New(apperr.ServerUnavailable) })
	out, _ := p.b.e.SendText(p.id, "не уйдёт")
	if n := p.b.e.ChatClosed(p.id); n != 3 {
		t.Fatalf("затронуто %d", n)
	}
	for _, m := range p.b.incoming() {
		want := localdb.StateUndelivered
		if m.Seq == pending[0].Seq {
			want = localdb.StateKeyMissing
		}
		if m.State != want {
			t.Fatalf("%d: %d", m.Seq, m.State)
		}
	}
	if m := p.b.msg(out.Seq); m.State != localdb.StateUndelivered {
		t.Fatal(m.State)
	}
}
