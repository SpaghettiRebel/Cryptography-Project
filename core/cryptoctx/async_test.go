package cryptoctx

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cryptochat/core/cipher"
	"cryptochat/core/modes"
)

// slowCipher — заглушка с задержкой: нужна только для проверки отмены.
type slowCipher struct {
	delay time.Duration
	calls atomic.Int64
}

func (s *slowCipher) SetKey([]byte) error { return nil }
func (s *slowCipher) BlockSize() int      { return 16 }
func (s *slowCipher) EncryptBlock(b []byte) ([]byte, error) {
	s.calls.Add(1)
	time.Sleep(s.delay)
	out := make([]byte, 16)
	for i := range out {
		out[i] = b[i] ^ 0x5A
	}
	return out, nil
}
func (s *slowCipher) DecryptBlock(b []byte) ([]byte, error) { return s.EncryptBlock(b) }

var _ cipher.BlockCipher = (*slowCipher)(nil)

func TestAsync_DeliversResultAndCloses(t *testing.T) {
	c := newCtx(t, modes.CTR, modes.PKCS7)
	data := randomBytes(t, 1000)
	want, _ := c.Encrypt(data)

	res, ok := <-c.EncryptAsync(context.Background(), data)
	if !ok || res.Err != nil || !bytes.Equal(res.Data, want) {
		t.Fatalf("EncryptAsync: ok=%v err=%v", ok, res.Err)
	}
	ch := c.DecryptAsync(context.Background(), res.Data)
	back := <-ch
	if back.Err != nil || !bytes.Equal(back.Data, data) {
		t.Fatalf("DecryptAsync: %v", back.Err)
	}
	if _, open := <-ch; open {
		t.Fatal("канал должен закрыться после единственного результата")
	}
}

func TestAsync_CancellationStopsWork(t *testing.T) {
	const blocks = 20000
	data := make([]byte, 16*blocks)
	for _, tc := range []struct {
		name string
		mode modes.CipherMode
	}{
		{"параллельный ECB", modes.ECB},
		{"последовательный CBC", modes.CBC},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cipher := &slowCipher{delay: time.Millisecond}
			c, err := New(cipher, nil, tc.mode, modes.PKCS7, WithWorkers(4))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			ch := c.EncryptAsync(ctx, data)
			time.Sleep(30 * time.Millisecond)
			cancel()

			start := time.Now()
			res := <-ch
			if !errors.Is(res.Err, context.Canceled) {
				t.Fatalf("ошибка %v, ожидалась context.Canceled", res.Err)
			}
			if res.Data != nil {
				t.Fatal("отменённая операция не должна возвращать данные")
			}
			if waited := time.Since(start); waited > 2*time.Second {
				t.Fatalf("отмена заняла %v", waited)
			}
			if n := cipher.calls.Load(); n >= blocks {
				t.Fatalf("обработаны все %d блоков, несмотря на отмену", n)
			}
		})
	}
}

func TestAsync_DeadlineExceeded(t *testing.T) {
	c, _ := New(&slowCipher{delay: time.Millisecond}, nil, modes.OFB, modes.PKCS7)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	res := <-c.DecryptAsync(ctx, make([]byte, 16*5000))
	if !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Fatalf("ошибка %v, ожидалась context.DeadlineExceeded", res.Err)
	}
}

func TestFiles_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.bin")
	enc := filepath.Join(dir, "enc.bin")
	dec := filepath.Join(dir, "dec.bin")
	data := randomBytes(t, 32*1024+13)
	if err := os.WriteFile(in, data, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, mode := range modes.AllCipherModes {
		c := newCtx(t, mode, modes.ISO10126)
		if err := c.EncryptFile(in, enc); err != nil {
			t.Fatalf("%s: EncryptFile: %v", mode, err)
		}
		encData, _ := os.ReadFile(enc)
		if bytes.HasPrefix(encData, data[:64]) {
			t.Fatalf("%s: файл шифротекста начинается с открытого текста", mode)
		}
		if err := c.DecryptFile(enc, dec); err != nil {
			t.Fatalf("%s: DecryptFile: %v", mode, err)
		}
		got, _ := os.ReadFile(dec)
		if !bytes.Equal(got, data) {
			t.Fatalf("%s: расшифрованный файл отличается", mode)
		}
	}
}

func TestFiles_AsyncAndErrors(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.bin")
	enc := filepath.Join(dir, "enc.bin")
	dec := filepath.Join(dir, "dec.bin")
	data := randomBytes(t, 4096)
	if err := os.WriteFile(in, data, 0o644); err != nil {
		t.Fatal(err)
	}
	c := newCtx(t, modes.PCBC, modes.ANSIX923)

	if res := <-c.EncryptFileAsync(context.Background(), in, enc); res.Err != nil || res.Data != nil {
		t.Fatalf("EncryptFileAsync: err=%v data=%v", res.Err, res.Data)
	}
	if res := <-c.DecryptFileAsync(context.Background(), enc, dec); res.Err != nil {
		t.Fatalf("DecryptFileAsync: %v", res.Err)
	}
	got, _ := os.ReadFile(dec)
	if !bytes.Equal(got, data) {
		t.Fatal("асинхронный проход файла дал другие данные")
	}

	if err := c.EncryptFile(filepath.Join(dir, "missing.bin"), enc); err == nil {
		t.Error("отсутствующий входной файл должен давать ошибку")
	}
	if err := c.EncryptFile(in, filepath.Join(dir, "no-such-dir", "x.bin")); err == nil {
		t.Error("недоступный выходной путь должен давать ошибку")
	}
}
