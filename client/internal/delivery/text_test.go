//go:build devcipher

package delivery

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cryptochat/client/internal/ciphers"
	"cryptochat/client/internal/keyagree"
	"cryptochat/client/internal/localdb"
	"cryptochat/core/cipher"
	"cryptochat/core/protocol"
)

// capture запоминает все отправленные фрагменты отправителя.
type capture struct {
	mu    sync.Mutex
	wires [][]byte
}

func (c *capture) hook(_ int, wire []byte) (bool, error) {
	c.mu.Lock()
	c.wires = append(c.wires, append([]byte(nil), wire...))
	c.mu.Unlock()
	return true, nil
}

func (c *capture) frags(t *testing.T) []*protocol.Fragment {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*protocol.Fragment
	for _, w := range c.wires {
		f, err := protocol.Unmarshal(w)
		if err != nil {
			t.Fatalf("отправленная часть не разбирается: %v", err)
		}
		out = append(out, f)
	}
	return out
}

// marker — текст длины n с меткой в начале.
func marker(n int, tag string) string {
	base := "МЕТКА-" + tag + "-"
	if n < len(base) {
		return strings.Repeat("x", n)
	}
	return base + strings.Repeat("ы", (n-len(base))/2) + strings.Repeat("z", (n-len(base))%2)
}

var lengths = []int{1, 15, 16, 17, 49151, 49152, 49153}

// Текст доходит символ в символ при всех режимах и набивках, IV у каждой части свой, в шифротексте
// нет открытого текста, части записаны у получателя до подтверждения.
func TestTextAllModes(t *testing.T) {
	type combo struct{ mode, pad int }
	var combos []combo
	for m := 1; m <= 7; m++ {
		for p := 1; p <= 4; p++ {
			combos = append(combos, combo{m, p})
		}
	}
	if testing.Short() {
		combos = []combo{{2, 3}, {1, 1}}
	}
	for i, c := range combos {
		bits := []int{128, 192, 256}[i%3]
		t.Run(fmt.Sprintf("mode%d-pad%d-%d", c.mode, c.pad, bits), func(t *testing.T) {
			p := newPair(t, c.mode, c.pad, bits)
			cap := &capture{}
			p.a.api.SetSendHook(cap.hook)
			var ackMiss, acks atomic.Int32
			p.srv.OnAck(func(id [16]byte, login string, uid uint64) {
				if login != "bob" {
					return
				}
				// К моменту подтверждения в базе получателя записано не меньше частей, чем подтверждено.
				n := acks.Add(1)
				stored := 0
				for _, m := range p.b.incoming() {
					stored += m.ReceivedCount
				}
				if int32(stored) < n {
					ackMiss.Add(1)
				}
			})
			p.a.receive()
			p.b.receive()
			texts := map[string]bool{}
			for _, n := range lengths {
				text := marker(n, fmt.Sprint(n))
				if n == 49151 {
					text = text[:len(text)-3] + " \n\n" // пробелы и переводы строк в конце сохраняются
				}
				if _, err := p.a.e.SendText(p.id, text); err != nil {
					t.Fatal(err)
				}
				texts[text] = true
			}
			got := p.b.waitIncoming(len(lengths), 20*time.Second, localdb.StateDone)
			for _, m := range got {
				if m.Kind != localdb.KindText || !texts[m.Text] {
					t.Fatalf("текст не совпал: вид %d, длина %d", m.Kind, len(m.Text))
				}
				delete(texts, m.Text)
			}
			if ackMiss.Load() > 0 {
				t.Fatal("часть подтверждена раньше записи в базу")
			}
			frags := cap.frags(t)
			ivs := map[string]bool{}
			needsIV := c.mode != 1
			for _, f := range frags {
				if needsIV != (len(f.IV) == 16) || (!needsIV && len(f.IV) != 0) {
					t.Fatalf("IV длины %d при режиме %d", len(f.IV), c.mode)
				}
				if needsIV {
					if ivs[string(f.IV)] {
						t.Fatal("IV повторился")
					}
					ivs[string(f.IV)] = true
				}
				if bytes.Contains(f.Cipher, []byte("МЕТКА-")) {
					t.Fatal("открытый текст в шифротексте")
				}
			}
			out, _, _ := p.a.db.ListMessages(p.id, 0, 100)
			for _, m := range out {
				if m.State != localdb.StateSent || m.SentCount != m.Total {
					t.Fatalf("отправитель: %+v", m.State)
				}
				if w, ok, _ := p.a.db.OutFragment(m.Seq, 0); ok || w != nil {
					t.Fatal("спул не пуст после SENT")
				}
			}
		})
	}
}

// Два одинаковых текста — разные message_id и разные IV; сообщения чата уходят в порядке номеров.
func TestSameTextTwiceAndOrder(t *testing.T) {
	p := newPair(t, 2, 3, 128)
	cap := &capture{}
	p.a.api.SetSendHook(cap.hook)
	p.a.receive()
	p.b.receive()
	var seqs []int64
	for i := 0; i < 5; i++ {
		v, err := p.a.e.SendText(p.id, "одинаковый")
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, v.Seq)
	}
	p.b.waitIncoming(5, 10*time.Second, localdb.StateDone)
	frags := cap.frags(t)
	ids := map[[16]byte]bool{}
	for i, f := range frags {
		want := p.a.msg(seqs[i]).MessageID
		if f.MessageID != want {
			t.Fatal("сообщения ушли не по порядку номеров")
		}
		ids[f.MessageID] = true
	}
	if len(ids) != 5 || bytes.Equal(frags[0].IV, frags[1].IV) || bytes.Equal(frags[0].Cipher, frags[1].Cipher) {
		t.Fatal("одинаковые тексты дали одинаковые идентификаторы или шифротекст")
	}
}

type countingWriter struct {
	w       io.WriterAt
	cur     atomic.Int32
	maxSeen atomic.Int32
}

func (c *countingWriter) WriteAt(p []byte, off int64) (int, error) {
	n := c.cur.Add(1)
	for {
		m := c.maxSeen.Load()
		if n <= m || c.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(30 * time.Millisecond)
	defer c.cur.Add(-1)
	return c.w.WriteAt(p, off)
}

func writeFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "исходный файл.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func sendFile(t *testing.T, s *side, chat [16]byte, data []byte) MessageView {
	t.Helper()
	path := writeFile(t, data)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.e.SendFile(chat, path, st.Size(), st.ModTime())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Части одного сообщения записываются несколькими горутинами одновременно (FR-004).
func TestParallelAssembly(t *testing.T) {
	var cw *countingWriter
	var once sync.Once
	wrapWriter = func(w io.WriterAt) io.WriterAt {
		once.Do(func() { cw = &countingWriter{w: w} })
		if cw.w != w {
			cw.w = w
		}
		return cw
	}
	t.Cleanup(func() { wrapWriter = func(w io.WriterAt) io.WriterAt { return w } })
	p := newPair(t, 6, 4, 256)
	p.a.receive()
	p.b.receive()
	data := randomBytes(3*localdb.ChunkSize - 100)
	sendFile(t, p.a, p.id, data)
	got := p.b.waitIncoming(1, 10*time.Second, localdb.StateDone)
	if !bytes.Equal(p.b.content(got[0].Seq), data) {
		t.Fatal("содержимое не совпало")
	}
	if cw.maxSeen.Load() < 2 {
		t.Fatalf("WriteAt не вызывался одновременно: %d", cw.maxSeen.Load())
	}
}

// Результат многопоточной и однопоточной обработки одинаков (принцип V).
func TestSingleAndMultiThreaded(t *testing.T) {
	data := randomBytes(5*localdb.ChunkSize - 7)
	run := func(procs int) []byte {
		prev := runtime.GOMAXPROCS(procs)
		defer runtime.GOMAXPROCS(prev)
		p := newPair(t, 7, 2, 192)
		p.a.receive()
		p.b.receive()
		sendFile(t, p.a, p.id, data)
		got := p.b.waitIncoming(1, 15*time.Second, localdb.StateDone)
		return p.b.content(got[0].Seq)
	}
	one, many := run(1), run(runtime.NumCPU())
	if !bytes.Equal(one, data) || !bytes.Equal(many, data) {
		t.Fatal("результаты однопоточной и многопоточной обработки различаются")
	}
}

// Часть пришла, когда ключ получателя ещё не выработан: сообщение ждёт и расшифровывается после.
func TestDecryptWaitsForKey(t *testing.T) {
	p := newPair(t, 2, 3, 128)
	p.b.mu.Lock()
	p.b.key = keyagree.WaitingPeer
	p.b.mu.Unlock()
	p.a.receive()
	p.b.receive()
	if _, err := p.a.e.SendText(p.id, "ждёт ключа"); err != nil {
		t.Fatal(err)
	}
	got := p.b.waitIncoming(1, 5*time.Second, localdb.StateDecryptPending)
	time.Sleep(100 * time.Millisecond)
	if m := p.b.msg(got[0].Seq); m.State != localdb.StateDecryptPending || m.ErrorCode != "" {
		t.Fatalf("без ключа: %d %q", m.State, m.ErrorCode)
	}
	p.b.mu.Lock()
	p.b.key = keyagree.Ready
	p.b.mu.Unlock()
	p.b.e.Sync(map[[16]byte]bool{p.id: true})
	m := p.b.waitState(got[0].Seq, 5*time.Second, localdb.StateDone)
	if m.Text != "ждёт ключа" {
		t.Fatal(m.Text)
	}
}

// Текст в чате с алгоритмом MAGENTA (в этой сборке — тестовая замена) в двух режимах: части уходят
// шифротекстом и собираются у получателя символ в символ.
func TestTextMagentaSubstitute(t *testing.T) {
	impl, err := ciphers.Resolve(cipher.MAGENTA)
	if err != nil || impl == cipher.MAGENTA {
		t.Fatalf("ожидалась тестовая замена MAGENTA: %v %v", impl, err)
	}
	for _, c := range []struct{ mode, pad, bits int }{{2, 3, 128}, {6, 1, 256}} {
		t.Run(fmt.Sprintf("mode%d-pad%d-%d", c.mode, c.pad, c.bits), func(t *testing.T) {
			p := newPairAlg(t, cipher.MAGENTA, c.mode, c.pad, c.bits)
			cap := &capture{}
			p.a.api.SetSendHook(cap.hook)
			p.a.receive()
			p.b.receive()
			texts := map[string]bool{}
			for _, n := range []int{1, 17, 49153} {
				text := marker(n, fmt.Sprint(n))
				if _, err := p.a.e.SendText(p.id, text); err != nil {
					t.Fatal(err)
				}
				texts[text] = true
			}
			for _, m := range p.b.waitIncoming(len(texts), 20*time.Second, localdb.StateDone) {
				if m.Kind != localdb.KindText || !texts[m.Text] {
					t.Fatalf("текст не совпал: вид %d, длина %d", m.Kind, len(m.Text))
				}
				delete(texts, m.Text)
			}
			for _, f := range cap.frags(t) {
				if bytes.Contains(f.Cipher, []byte("МЕТКА-")) {
					t.Fatal("открытый текст в шифротексте")
				}
			}
		})
	}
}
