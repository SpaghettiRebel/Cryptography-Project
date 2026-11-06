//go:build devcipher

package algorithms

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"cryptochat/core/cipher"
)

func TestDevCipherAvailable(t *testing.T) {
	if !slices.Contains(Available(), DevAlgorithm) {
		t.Fatalf("Available() = %v", Available())
	}
	c, err := New(DevAlgorithm, 128)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetKey(make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	block := bytes.Repeat([]byte{3}, 16)
	ct, err := c.EncryptBlock(block)
	if err != nil {
		t.Fatal(err)
	}
	back, err := c.DecryptBlock(ct)
	if err != nil || !bytes.Equal(back, block) {
		t.Fatalf("обратимость нарушена: %x, %v", back, err)
	}
	if _, err := New(DevAlgorithm, 100); !errors.Is(err, cipher.ErrInvalidKeySize) {
		t.Errorf("недопустимый keyBits: %v", err)
	}
}
