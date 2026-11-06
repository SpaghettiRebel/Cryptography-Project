package cryptoctx

import (
	"crypto/rand"
	"encoding/hex"
	"testing"

	"cryptochat/core/internal/stubcipher"
	"cryptochat/core/modes"
)

var testKey = unhex("2b7e151628aed2a6abf7158809cf4f3c")

func unhex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// newStub — тестовый шифр с ключом 128 бит.
func newStub(t *testing.T) *stubcipher.Cipher {
	t.Helper()
	c, err := stubcipher.New(128)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// newCtx — контекст на тестовом шифре с ключом 128 бит.
func newCtx(t *testing.T, mode modes.CipherMode, pad modes.PaddingMode, opts ...Option) *CryptoContext {
	t.Helper()
	c, err := New(newStub(t), testKey, mode, pad, opts...)
	if err != nil {
		t.Fatalf("New(%s, %s): %v", mode, pad, err)
	}
	return c
}

// ivOption возвращает опцию IV для режимов, которым он нужен.
func ivOption(mode modes.CipherMode, iv []byte) []Option {
	if mode.NeedsIV() {
		return []Option{WithIV(iv)}
	}
	return nil
}
