package stubcipher

import (
	"bytes"
	"crypto/rand"
	"errors"
	"sync"
	"testing"

	"cryptochat/core/cipher"
)

var _ cipher.BlockCipher = (*Cipher)(nil)

func keyed(t *testing.T, keyBits int) *Cipher {
	t.Helper()
	c, err := New(keyBits)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, keyBits/8)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if err := c.SetKey(key); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRoundTrip(t *testing.T) {
	for _, bits := range []int{128, 192, 256} {
		c := keyed(t, bits)
		for i := 0; i < 1000; i++ {
			pt := make([]byte, 16)
			if _, err := rand.Read(pt); err != nil {
				t.Fatal(err)
			}
			orig := append([]byte(nil), pt...)
			ct, err := c.EncryptBlock(pt)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(pt, orig) {
				t.Fatal("шифрование изменило входной срез")
			}
			back, err := c.DecryptBlock(ct)
			if err != nil || !bytes.Equal(back, pt) {
				t.Fatalf("обратимость нарушена: %x, %v", back, err)
			}
		}
	}
}

func TestKeysDiffer(t *testing.T) {
	a, b := keyed(t, 128), keyed(t, 128)
	block := make([]byte, 16)
	x, _ := a.EncryptBlock(block)
	y, _ := b.EncryptBlock(block)
	if bytes.Equal(x, y) {
		t.Fatal("разные ключи дали одинаковый результат")
	}
}

func TestValidation(t *testing.T) {
	if _, err := New(100); !errors.Is(err, cipher.ErrInvalidKeySize) {
		t.Errorf("New(100): %v", err)
	}
	c, err := New(128)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.EncryptBlock(make([]byte, 16)); !errors.Is(err, cipher.ErrKeyNotSet) {
		t.Errorf("без ключа: %v", err)
	}
	if _, err := c.DecryptBlock(make([]byte, 16)); !errors.Is(err, cipher.ErrKeyNotSet) {
		t.Errorf("без ключа: %v", err)
	}
	if err := c.SetKey(make([]byte, 5)); !errors.Is(err, cipher.ErrInvalidKeySize) {
		t.Errorf("короткий ключ: %v", err)
	}
	if err := c.SetKey(make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EncryptBlock(make([]byte, 5)); !errors.Is(err, cipher.ErrInvalidBlockSize) {
		t.Errorf("короткий блок: %v", err)
	}
	if _, err := c.DecryptBlock(make([]byte, 17)); !errors.Is(err, cipher.ErrInvalidBlockSize) {
		t.Errorf("длинный блок: %v", err)
	}
}

func TestConcurrentUse(t *testing.T) {
	c := keyed(t, 256)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			block := bytes.Repeat([]byte{byte(g)}, 16)
			for i := 0; i < 1000; i++ {
				ct, err := c.EncryptBlock(block)
				if err != nil {
					t.Error(err)
					return
				}
				back, err := c.DecryptBlock(ct)
				if err != nil || !bytes.Equal(back, block) {
					t.Errorf("расхождение: %x, %v", back, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestLoadKeepsResult(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 16)
	plain, _ := New(128)
	loaded, err := NewWithLoad(128, 50)
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.SetKey(key); err != nil {
		t.Fatal(err)
	}
	if err := loaded.SetKey(key); err != nil {
		t.Fatal(err)
	}
	block := make([]byte, 16)
	x, _ := plain.EncryptBlock(block)
	y, _ := loaded.EncryptBlock(block)
	if !bytes.Equal(x, y) {
		t.Fatal("нагрузка не должна менять результат")
	}
}
