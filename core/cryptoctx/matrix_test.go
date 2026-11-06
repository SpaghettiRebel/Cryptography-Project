package cryptoctx

import (
	"bytes"
	"fmt"
	"testing"

	"cryptochat/core/internal/stubcipher"
	"cryptochat/core/modes"
)

func nonZeroTail(t *testing.T, n int) []byte {
	t.Helper()
	b := randomBytes(t, n)
	if n > 0 {
		b[n-1] |= 1
	}
	return b
}

func TestMatrix(t *testing.T) {
	for _, keyBits := range []int{128, 192, 256} {
		key := randomBytes(t, keyBits/8)
		for _, mode := range modes.AllCipherModes {
			for _, pad := range modes.AllPaddingModes {
				for _, n := range []int{0, 1, 15, 16, 17} {
					name := fmt.Sprintf("%d/%s/%s/%d", keyBits, mode, pad, n)
					t.Run(name, func(t *testing.T) {
						data := nonZeroTail(t, n)
						c, err := stubcipher.New(keyBits)
						if err != nil {
							t.Fatal(err)
						}
						ctx, err := New(c, key, mode, pad)
						if err != nil {
							t.Fatal(err)
						}
						ct, err := ctx.Encrypt(data)
						if err != nil {
							t.Fatal(err)
						}
						pt, err := ctx.Decrypt(ct)
						if err != nil || !bytes.Equal(pt, data) {
							t.Fatalf("расшифрование: %x, %v", pt, err)
						}
					})
				}
			}
		}
	}
}

func TestDifferentIVDifferentCiphertext(t *testing.T) {
	key := randomBytes(t, 16)
	data := nonZeroTail(t, 40)
	for _, mode := range modes.AllCipherModes {
		if !mode.NeedsIV() {
			continue
		}
		var results [2][]byte
		for i := range results {
			c, err := stubcipher.New(128)
			if err != nil {
				t.Fatal(err)
			}
			ctx, err := New(c, key, mode, modes.PKCS7)
			if err != nil {
				t.Fatal(err)
			}
			if results[i], err = ctx.Encrypt(data); err != nil {
				t.Fatal(err)
			}
		}
		if bytes.Equal(results[0], results[1]) {
			t.Errorf("%s: разные IV дали одинаковый шифротекст", mode)
		}
	}
}
