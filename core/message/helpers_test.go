package message

import (
	"bytes"
	"context"
	"crypto/rand"
	"sync"
	"testing"

	"cryptochat/core/cipher"
	"cryptochat/core/internal/stubcipher"
	"cryptochat/core/modes"
	"cryptochat/core/protocol"
)

var testMessageID = [protocol.IDSize]byte{0xE1, 0xE2}

func testParams(t *testing.T, mode modes.CipherMode, pad modes.PaddingMode) Params {
	t.Helper()
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return Params{
		Settings: cipher.Settings{Algorithm: cipher.RC6, KeyBits: 128, Mode: mode, Padding: pad},
		Key:      key,
		ChatID:   [protocol.IDSize]byte{0xC1, 0xC2},
	}
}

func stubFor(t *testing.T, p Params) cipher.BlockCipher {
	t.Helper()
	c, err := stubcipher.New(p.Settings.KeyBits)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetKey(p.Key); err != nil {
		t.Fatal(err)
	}
	return c
}

func testConfig(t *testing.T, p Params) sealConfig {
	t.Helper()
	return sealConfig{cipher: stubFor(t, p), rand: rand.Reader, workers: defaultWorkers()}
}

// sealAll шифрует сообщение на тестовом шифре и возвращает фрагменты.
func sealAll(t *testing.T, p Params, meta Meta, data []byte) []*protocol.Fragment {
	t.Helper()
	var out []*protocol.Fragment
	err := seal(context.Background(), testConfig(t, p), p, testMessageID, meta, bytes.NewReader(data),
		func(f *protocol.Fragment) error {
			out = append(out, f)
			return nil
		}, nil)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return out
}

func newTestAssembler(t *testing.T, p Params, w *memWriter) *Assembler {
	t.Helper()
	a, err := newAssembler(stubFor(t, p), p, testMessageID, w, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// memWriter — io.WriterAt в памяти, безопасный при одновременной записи.
type memWriter struct {
	mu   sync.Mutex
	data []byte
}

func (m *memWriter) WriteAt(p []byte, off int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if end := off + int64(len(p)); end > int64(len(m.data)) {
		m.data = append(m.data, make([]byte, end-int64(len(m.data)))...)
	}
	copy(m.data[off:], p)
	return len(p), nil
}

// discardWriter отбрасывает данные.
type discardWriter struct{}

func (discardWriter) WriteAt(p []byte, _ int64) (int, error) { return len(p), nil }

// detReader — детерминированный источник для сравнения результатов.
type detReader struct{ state uint64 }

func (d *detReader) Read(p []byte) (int, error) {
	for i := range p {
		d.state ^= d.state << 13
		d.state ^= d.state >> 7
		d.state ^= d.state << 17
		p[i] = byte(d.state)
	}
	return len(p), nil
}

// patternReader отдаёт n байт известного шаблона без выделения памяти.
type patternReader struct {
	left int64
	pos  byte
}

func (r *patternReader) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, ioEOF
	}
	n := int64(len(p))
	if n > r.left {
		n = r.left
	}
	for i := int64(0); i < n; i++ {
		p[i] = r.pos
		r.pos += 31
	}
	r.left -= n
	return int(n), nil
}

// encryptInner собирает фрагмент из произвольной внутренней структуры для проверок нарушений.
func encryptInner(t *testing.T, p Params, in *inner, index, total uint32) *protocol.Fragment {
	t.Helper()
	plain, err := in.build(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var iv []byte
	if p.Settings.Mode.NeedsIV() {
		iv = make([]byte, protocol.IVSize)
		if _, err := rand.Read(iv); err != nil {
			t.Fatal(err)
		}
	}
	f, err := encryptFragment(context.Background(), stubFor(t, p), p, testMessageID, index, total, plain, iv)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
