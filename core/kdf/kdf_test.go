package kdf

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"

	"cryptochat/core/cipher"
)

func h(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// RFC 5869, приложение A.1, значения сверены с текстом RFC.
func TestHKDFRFC5869(t *testing.T) {
	got, err := HKDF(bytes.Repeat([]byte{0x0b}, 22),
		h("000102030405060708090a0b0c"),
		h("f0f1f2f3f4f5f6f7f8f9"), 42)
	if err != nil {
		t.Fatal(err)
	}
	want := h("3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865")
	if !bytes.Equal(got, want) {
		t.Fatalf("OKM %x", got)
	}
}

func TestHKDFLimits(t *testing.T) {
	if _, err := HKDF([]byte{1}, nil, nil, 0); err == nil {
		t.Error("длина 0 должна отвергаться")
	}
	if _, err := HKDF([]byte{1}, nil, nil, 255*32+1); err == nil {
		t.Error("длина больше 255 блоков должна отвергаться")
	}
	if out, err := HKDF([]byte{1}, nil, nil, 255*32); err != nil || len(out) != 255*32 {
		t.Errorf("предельная длина: %d, %v", len(out), err)
	}
}

func TestSessionKey(t *testing.T) {
	shared := bytes.Repeat([]byte{0x5A}, SharedSecretSize)
	chat := [16]byte{1, 2, 3}
	base, err := SessionKey(shared, chat, cipher.RC6, 256)
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 32 {
		t.Fatalf("длина %d", len(base))
	}
	again, _ := SessionKey(shared, chat, cipher.RC6, 256)
	if !bytes.Equal(base, again) {
		t.Fatal("вывод должен быть детерминированным")
	}
	for _, bits := range []int{128, 192} {
		k, err := SessionKey(shared, chat, cipher.RC6, bits)
		if err != nil || len(k) != bits/8 {
			t.Errorf("%d бит: длина %d, %v", bits, len(k), err)
		}
	}
	otherChat, _ := SessionKey(shared, [16]byte{9}, cipher.RC6, 256)
	otherAlg, _ := SessionKey(shared, chat, cipher.MAGENTA, 256)
	otherBits, _ := SessionKey(shared, chat, cipher.RC6, 192)
	otherShared, _ := SessionKey(bytes.Repeat([]byte{1}, SharedSecretSize), chat, cipher.RC6, 256)
	for name, k := range map[string][]byte{"чат": otherChat, "алгоритм": otherAlg, "секрет": otherShared} {
		if bytes.Equal(k, base) {
			t.Errorf("%s: ключ не изменился", name)
		}
	}
	if bytes.Equal(otherBits, base[:24]) {
		t.Error("длина ключа должна входить в контекст")
	}
	if _, err := SessionKey(shared[:10], chat, cipher.RC6, 256); !errors.Is(err, ErrInvalidSecret) {
		t.Errorf("короткий секрет: %v", err)
	}
	if _, err := SessionKey(shared, chat, cipher.RC6, 100); !errors.Is(err, cipher.ErrInvalidKeySize) {
		t.Errorf("недопустимая длина ключа: %v", err)
	}
}
