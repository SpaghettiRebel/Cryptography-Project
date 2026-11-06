package cryptoctx

import (
	"bytes"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"cryptochat/core/cipher"
	"cryptochat/core/internal/stubcipher"
	"cryptochat/core/modes"
)

// countingCipher считает вызовы SetKey.
type countingCipher struct {
	cipher.BlockCipher
	setKeys atomic.Int64
}

func (c *countingCipher) SetKey(key []byte) error {
	c.setKeys.Add(1)
	return c.BlockCipher.SetKey(key)
}

func TestNewWithCipher(t *testing.T) {
	key := randomBytes(t, 16)
	base, err := stubcipher.New(128)
	if err != nil {
		t.Fatal(err)
	}
	if err := base.SetKey(key); err != nil {
		t.Fatal(err)
	}
	counted := &countingCipher{BlockCipher: base}

	var wg sync.WaitGroup
	for _, mode := range modes.AllCipherModes {
		wg.Add(1)
		go func(mode modes.CipherMode) {
			defer wg.Done()
			iv := bytes.Repeat([]byte{0xA5}, 16)
			data := bytes.Repeat([]byte{0x11}, 1000)
			ctx, err := NewWithCipher(counted, mode, modes.PKCS7, ivOption(mode, iv)...)
			if err != nil {
				t.Error(err)
				return
			}
			got, err := ctx.Encrypt(data)
			if err != nil {
				t.Error(err)
				return
			}
			back, err := ctx.Decrypt(got)
			if err != nil || !bytes.Equal(back, data) {
				t.Errorf("%s: расшифрование: %v", mode, err)
				return
			}
			// Результат совпадает с обычным конструктором.
			own, err := stubcipher.New(128)
			if err != nil {
				t.Error(err)
				return
			}
			plain, err := New(own, key, mode, modes.PKCS7, ivOption(mode, iv)...)
			if err != nil {
				t.Error(err)
				return
			}
			want, err := plain.Encrypt(data)
			if err != nil {
				t.Error(err)
				return
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s: результат отличается от New", mode)
			}
		}(mode)
	}
	wg.Wait()
	if n := counted.setKeys.Load(); n != 0 {
		t.Fatalf("SetKey вызван %d раз", n)
	}
	if _, err := NewWithCipher(nil, modes.ECB, modes.PKCS7); err == nil {
		t.Error("шифр nil должен отвергаться")
	}
}

// shortCipher возвращает блок неверной длины.
type shortCipher struct{ cipher.BlockCipher }

func (s shortCipher) EncryptBlock(b []byte) ([]byte, error) {
	out, err := s.BlockCipher.EncryptBlock(b)
	if err != nil {
		return nil, err
	}
	return out[:len(out)-1], nil
}

func TestWrongResultLength(t *testing.T) {
	base, err := stubcipher.New(128)
	if err != nil {
		t.Fatal(err)
	}
	if err := base.SetKey(make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	for _, mode := range modes.AllCipherModes {
		ctx, err := NewWithCipher(shortCipher{base}, mode, modes.PKCS7, ivOption(mode, make([]byte, 16))...)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ctx.Encrypt(make([]byte, 16)); !errors.Is(err, cipher.ErrInvalidBlockSize) {
			t.Errorf("%s: %v", mode, err)
		}
	}
}
