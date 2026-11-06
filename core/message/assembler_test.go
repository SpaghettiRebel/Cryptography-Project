package message

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"cryptochat/core/modes"
	"cryptochat/core/protocol"
)

var bg = context.Background()

func twoChunkMessage(t *testing.T) (Params, Meta, []byte, []*protocol.Fragment) {
	t.Helper()
	p := testParams(t, modes.CBC, modes.PKCS7)
	data := make([]byte, 2*ChunkSize+10)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	meta := Meta{Name: "a.bin", Type: "t", TotalSize: int64(len(data))}
	return p, meta, data, sealAll(t, p, meta, data)
}

func TestMissingThenComplete(t *testing.T) {
	p, meta, data, frags := twoChunkMessage(t)
	out := &memWriter{}
	a := newTestAssembler(t, p, out)
	if _, err := a.Finish(); !errors.Is(err, ErrMissingFragments) {
		t.Fatalf("пустая сборка: %v", err)
	}
	for _, f := range frags[1:] {
		if err := a.Add(bg, f); err != nil {
			t.Fatal(err)
		}
	}
	// Фрагмента 0 с метаданными ещё нет.
	if _, err := a.Finish(); !errors.Is(err, ErrMissingFragments) {
		t.Fatalf("без фрагмента 0: %v", err)
	}
	if err := a.Add(bg, frags[0]); err != nil {
		t.Fatal(err)
	}
	got, err := a.Finish()
	if err != nil || got != meta || !bytes.Equal(out.data, data) {
		t.Fatalf("результат: %+v, %v", got, err)
	}
}

func TestRepeatIgnored(t *testing.T) {
	p, _, data, frags := twoChunkMessage(t)
	out := &memWriter{}
	a := newTestAssembler(t, p, out)
	for _, f := range append(frags, frags...) {
		if err := a.Add(bg, f); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Finish(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.data, data) {
		t.Fatal("повтор исказил данные")
	}
}

func TestForeignFragments(t *testing.T) {
	p, _, _, frags := twoChunkMessage(t)
	cases := map[string]func(*protocol.Fragment){
		"чужой чат":       func(f *protocol.Fragment) { f.ChatID[0] ^= 1 },
		"чужое сообщение": func(f *protocol.Fragment) { f.MessageID[0] ^= 1 },
		"подмена номера":  func(f *protocol.Fragment) { f.Index = 2 },
		"другое Total":    func(f *protocol.Fragment) { f.Total = 9 },
		"без IV":          func(f *protocol.Fragment) { f.IV = nil },
		"длина шифра":     func(f *protocol.Fragment) { f.Cipher = f.Cipher[:len(f.Cipher)-1] },
	}
	for name, mutate := range cases {
		c := *frags[0]
		c.Cipher = append([]byte(nil), c.Cipher...)
		mutate(&c)
		a := newTestAssembler(t, p, &memWriter{})
		if err := a.Add(bg, &c); !errors.Is(err, ErrFragmentMismatch) {
			t.Errorf("%s: %v", name, err)
		}
	}
	a := newTestAssembler(t, p, &memWriter{})
	if err := a.Add(bg, nil); !errors.Is(err, ErrFragmentMismatch) {
		t.Errorf("nil: %v", err)
	}
}

func TestInnerBindingViolations(t *testing.T) {
	p := testParams(t, modes.CTR, modes.PKCS7)
	chunk := make([]byte, ChunkSize)
	good := func() *inner {
		return &inner{chatID: p.ChatID, messageID: testMessageID, totalSize: ChunkSize + 5, data: chunk}
	}
	cases := map[string]struct {
		in           func() *inner
		index, total uint32
	}{
		"чужой чат внутри":    {func() *inner { i := good(); i.chatID[0] ^= 1; return i }, 0, 2},
		"чужое сообщение":     {func() *inner { i := good(); i.messageID[0] ^= 1; return i }, 0, 2},
		"номер не совпал":     {func() *inner { i := good(); i.index = 1; return i }, 0, 2},
		"Total не по размеру": {func() *inner { i := good(); return i }, 0, 3},
		"кусок не 1 МиБ":      {func() *inner { i := good(); i.data = chunk[:100]; return i }, 0, 2},
		"последний не тот":    {func() *inner { i := good(); i.index = 1; i.data = chunk[:6]; return i }, 1, 2},
		"имя не во фрагменте 0": {func() *inner {
			i := good()
			i.index = 1
			i.data = chunk[:5]
			i.name = "x"
			return i
		}, 1, 2},
	}
	for name, c := range cases {
		a := newTestAssembler(t, p, &memWriter{})
		f := encryptInner(t, p, c.in(), c.index, c.total)
		if err := a.Add(bg, f); !errors.Is(err, ErrFragmentMismatch) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTotalSizeMustMatchAcrossFragments(t *testing.T) {
	p := testParams(t, modes.CTR, modes.PKCS7)
	a := newTestAssembler(t, p, &memWriter{})
	first := &inner{chatID: p.ChatID, messageID: testMessageID, totalSize: ChunkSize + 5, data: make([]byte, ChunkSize)}
	if err := a.Add(bg, encryptInner(t, p, first, 0, 2)); err != nil {
		t.Fatal(err)
	}
	other := &inner{chatID: p.ChatID, messageID: testMessageID, index: 1, totalSize: ChunkSize + 6, data: make([]byte, 6)}
	if err := a.Add(bg, encryptInner(t, p, other, 1, 2)); !errors.Is(err, ErrFragmentMismatch) {
		t.Fatalf("другой TotalSize: %v", err)
	}
}

func TestWrongKeyNoPanic(t *testing.T) {
	for _, mode := range modes.AllCipherModes {
		for _, pad := range modes.AllPaddingModes {
			p := testParams(t, mode, pad)
			frags := sealAll(t, p, Meta{TotalSize: 100}, make([]byte, 100))
			other := p
			other.Key = bytes.Repeat([]byte{0x77}, 16)
			a := newTestAssembler(t, other, &memWriter{})
			if err := a.Add(bg, frags[0]); err == nil {
				t.Errorf("%s/%s: неверный ключ должен дать ошибку", mode, pad)
			}
		}
	}
}

func TestConcurrentAdd(t *testing.T) {
	p := testParams(t, modes.CBC, modes.PKCS7)
	data := make([]byte, 6*ChunkSize+1)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	meta := Meta{Name: "n", TotalSize: int64(len(data))}
	frags := sealAll(t, p, meta, data)
	out := &memWriter{}
	a := newTestAssembler(t, p, out)
	var wg sync.WaitGroup
	for _, f := range append(frags, frags...) {
		wg.Add(1)
		go func(f *protocol.Fragment) {
			defer wg.Done()
			if err := a.Add(bg, f); err != nil {
				t.Error(err)
			}
		}(f)
	}
	wg.Wait()
	if got, err := a.Finish(); err != nil || got != meta {
		t.Fatalf("%+v, %v", got, err)
	}
	if !bytes.Equal(out.data, data) {
		t.Fatal("данные не совпали")
	}
}

type failingWriter struct{ fail bool }

func (w *failingWriter) WriteAt(p []byte, _ int64) (int, error) {
	if w.fail {
		return 0, errors.New("диск заполнен")
	}
	return len(p), nil
}

func TestWriteFailureAllowsRetry(t *testing.T) {
	p := testParams(t, modes.CTR, modes.PKCS7)
	frags := sealAll(t, p, Meta{TotalSize: 10}, make([]byte, 10))
	w := &failingWriter{fail: true}
	a, err := newAssembler(stubFor(t, p), p, testMessageID, w, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Add(bg, frags[0]); err == nil {
		t.Fatal("ошибка записи должна возвращаться")
	}
	w.fail = false
	if err := a.Add(bg, frags[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Finish(); err != nil {
		t.Fatal(err)
	}
}

func TestSealSizeErrors(t *testing.T) {
	p := testParams(t, modes.CTR, modes.PKCS7)
	drop := func(*protocol.Fragment) error { return nil }
	run := func(size int64, data []byte) error {
		return seal(bg, testConfig(t, p), p, testMessageID, Meta{TotalSize: size}, bytes.NewReader(data), drop, nil)
	}
	if err := run(10, make([]byte, 5)); !errors.Is(err, ErrSizeMismatch) {
		t.Errorf("короче: %v", err)
	}
	if err := run(10, make([]byte, 11)); !errors.Is(err, ErrSizeMismatch) {
		t.Errorf("длиннее: %v", err)
	}
	if err := run(0, []byte{1}); !errors.Is(err, ErrSizeMismatch) {
		t.Errorf("длиннее пустого: %v", err)
	}
	if err := run(-1, nil); !errors.Is(err, ErrSizeMismatch) {
		t.Errorf("отрицательный размер: %v", err)
	}
	if err := run(protocol.MaxTotal*ChunkSize+1, nil); !errors.Is(err, ErrSizeMismatch) {
		t.Errorf("больше 2²⁴ фрагментов: %v", err)
	}
}

func TestSealEmitError(t *testing.T) {
	p := testParams(t, modes.CTR, modes.PKCS7)
	boom := errors.New("канал закрыт")
	calls := 0
	err := seal(bg, testConfig(t, p), p, testMessageID, Meta{TotalSize: 5 * ChunkSize}, bytes.NewReader(make([]byte, 5*ChunkSize)),
		func(*protocol.Fragment) error {
			calls++
			return boom
		}, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("ошибка emit: %v", err)
	}
	if calls != 1 {
		t.Fatalf("emit вызван %d раз", calls)
	}
}

func TestSealRandomFailure(t *testing.T) {
	// Отказ при заполнении Fill: источник пуст сразу.
	p := testParams(t, modes.CBC, modes.PKCS7)
	cfg := testConfig(t, p)
	cfg.rand = bytes.NewReader(nil)
	err := seal(bg, cfg, p, testMessageID, Meta{TotalSize: 10}, bytes.NewReader(make([]byte, 10)),
		func(*protocol.Fragment) error { t.Error("фрагмента не должно быть"); return nil }, nil)
	if err == nil || !strings.Contains(err.Error(), "случайности") {
		t.Errorf("Fill: %v", err)
	}
	// Отказ при генерации IV: на Fill хватает, на IV — нет.
	cfg.rand = bytes.NewReader(make([]byte, 16))
	err = seal(bg, cfg, p, testMessageID, Meta{TotalSize: 10}, bytes.NewReader(make([]byte, 10)),
		func(*protocol.Fragment) error { t.Error("фрагмента не должно быть"); return nil }, nil)
	if err == nil || !strings.Contains(err.Error(), "случайности") {
		t.Errorf("IV: %v", err)
	}
}

func TestMetaLimits(t *testing.T) {
	p := testParams(t, modes.CTR, modes.PKCS7)
	drop := func(*protocol.Fragment) error { return nil }
	for name, meta := range map[string]Meta{
		"имя": {Name: strings.Repeat("x", MaxNameLen+1)},
		"тип": {Type: strings.Repeat("x", MaxTypeLen+1)},
	} {
		err := seal(bg, testConfig(t, p), p, testMessageID, meta, bytes.NewReader(nil), drop, nil)
		if !errors.Is(err, ErrInvalidMeta) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Предельные значения допустимы и влезают в MaxCipherLen даже с полным куском.
	meta := Meta{Name: strings.Repeat("x", MaxNameLen), Type: strings.Repeat("y", MaxTypeLen), TotalSize: ChunkSize}
	roundTrip(t, p, meta, make([]byte, ChunkSize))
}

// oneByteReader отдаёт данные по одному байту и иногда (0, nil): io.Reader это допускает.
type oneByteReader struct {
	data []byte
	tick int
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	r.tick++
	if r.tick%2 == 0 {
		return 0, nil
	}
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

func TestSealZeroLengthReads(t *testing.T) {
	p := testParams(t, modes.CTR, modes.PKCS7)
	drop := func(*protocol.Fragment) error { return nil }
	if err := seal(bg, testConfig(t, p), p, testMessageID, Meta{TotalSize: 3}, &oneByteReader{data: []byte{1, 2, 3}}, drop, nil); err != nil {
		t.Errorf("точный размер: %v", err)
	}
	if err := seal(bg, testConfig(t, p), p, testMessageID, Meta{TotalSize: 3}, &oneByteReader{data: []byte{1, 2, 3, 4}}, drop, nil); !errors.Is(err, ErrSizeMismatch) {
		t.Errorf("длиннее заявленного: %v", err)
	}
}
