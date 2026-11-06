package cryptoctx

import (
	"bytes"
	"testing"

	"cryptochat/core/modes"
)

func TestECB_IdenticalBlocks(t *testing.T) {
	c := newCtx(t, modes.ECB, modes.PKCS7)
	pt := bytes.Repeat([]byte("ABCDEFGHIJKLMNOP"), 3)
	ct, err := c.Encrypt(pt)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ct[:16], ct[16:32]) || !bytes.Equal(ct[16:32], ct[32:48]) {
		t.Fatalf("ECB должен одинаково шифровать одинаковые блоки: %x", ct)
	}
}

func TestNew_Validation(t *testing.T) {
	if _, err := New(nil, testKey, modes.ECB, modes.PKCS7); err == nil {
		t.Error("шифр nil должен отвергаться")
	}
	if _, err := New(newStub(t), []byte{1, 2, 3}, modes.ECB, modes.PKCS7); err == nil {
		t.Error("ключ неверной длины должен отвергаться")
	}
	if _, err := New(newStub(t), testKey, modes.CipherMode(42), modes.PKCS7); err == nil {
		t.Error("неизвестный режим должен отвергаться")
	}
	if _, err := New(newStub(t), testKey, modes.ECB, modes.PaddingMode(42)); err == nil {
		t.Error("неизвестная набивка должна отвергаться")
	}
	iv := make([]byte, 16)
	if _, err := New(newStub(t), testKey, modes.ECB, modes.PKCS7, WithIV(iv)); err == nil {
		t.Error("IV в ECB должен отвергаться")
	}
	if _, err := New(newStub(t), testKey, modes.CBC, modes.PKCS7, WithIV([]byte{1, 2, 3})); err == nil {
		t.Error("IV неверной длины должен отвергаться")
	}
	if _, err := New(newStub(t), testKey, modes.CBC, modes.PKCS7, WithWorkers(0)); err == nil {
		t.Error("ноль потоков должен отвергаться")
	}
	if _, err := New(newStub(t), testKey, modes.CTR, modes.PKCS7, WithDelta(make([]byte, 16))); err == nil {
		t.Error("WithDelta вне Random Delta должен отвергаться")
	}
	if _, err := New(newStub(t), testKey, modes.RandomDelta, modes.PKCS7, WithDelta([]byte{1})); err == nil {
		t.Error("дельта неверной длины должна отвергаться")
	}
	if _, err := New(newStub(t), testKey, modes.RandomDelta, modes.PKCS7, WithDelta(nil)); err == nil {
		t.Error("пустая дельта должна отвергаться")
	}
	c := newCtx(t, modes.ECB, modes.PKCS7)
	if c.IV() != nil || c.Delta() != nil {
		t.Error("у контекста ECB нет ни IV, ни дельты")
	}
}

func TestNew_IVHandling(t *testing.T) {
	for _, blockBits := range []int{128} {
		size := blockBits / 8
		a, err := New(newStub(t), testKey, modes.CBC, modes.PKCS7)
		if err != nil {
			t.Fatal(err)
		}
		b, err := New(newStub(t), testKey, modes.CBC, modes.PKCS7)
		if err != nil {
			t.Fatal(err)
		}
		if len(a.IV()) != size || bytes.Equal(a.IV(), b.IV()) {
			t.Fatalf("блок %d: ожидались разные случайные IV длины %d, получено %x и %x", blockBits, size, a.IV(), b.IV())
		}
	}
	iv := randomBytes(t, 16)
	saved := append([]byte(nil), iv...)
	c := newCtx(t, modes.CBC, modes.PKCS7, WithIV(iv))
	iv[0] ^= 0xFF
	c.IV()[1] ^= 0xFF
	if !bytes.Equal(c.IV(), saved) {
		t.Fatalf("IV разделяет память с внешним срезом: %x", c.IV())
	}
}

// Контекст вызывает SetKey у переданного шифра и владеет им; у контекстов на отдельных объектах
// шифра с разными ключами результаты независимы.
func TestContexts_SeparateCiphersAreIndependent(t *testing.T) {
	data := randomBytes(t, 64)
	a := newCtx(t, modes.ECB, modes.PKCS7)
	want, err := a.Encrypt(data)
	if err != nil {
		t.Fatal(err)
	}
	other, err := New(newStub(t), randomBytes(t, 16), modes.ECB, modes.PKCS7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Encrypt(data); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.Encrypt(data); !bytes.Equal(got, want) {
		t.Fatal("контекст с другим ключом повлиял на первый")
	}
}
