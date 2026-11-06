package cryptoctx

import (
	"bytes"
	"math/big"
	"testing"

	"cryptochat/core/modes"
)

func refCounter(base, delta []byte, i uint64) []byte {
	n := len(base)
	mod := new(big.Int).Lsh(big.NewInt(1), uint(8*n))
	v := new(big.Int).SetBytes(delta)
	v.Mul(v, new(big.Int).SetUint64(i))
	v.Add(v, new(big.Int).SetBytes(base))
	v.Mod(v, mod)
	return v.FillBytes(make([]byte, n))
}

func TestCounterAt_MatchesBigInt(t *testing.T) {
	for _, n := range []int{16, 24, 32} {
		for trial := 0; trial < 300; trial++ {
			base := randomBytes(t, n)
			delta := randomBytes(t, n)
			var i uint64
			for _, b := range randomBytes(t, 8) {
				i = i<<8 | uint64(b)
			}
			if trial%3 == 0 {
				i &= 0xFFFF
			}
			got := make([]byte, n)
			counterAt(got, base, delta, i)
			if want := refCounter(base, delta, i); !bytes.Equal(got, want) {
				t.Fatalf("n=%d base=%x delta=%x i=%d: %x, ожидалось %x", n, base, delta, i, got, want)
			}
		}
		// Переполнение: счётчик идёт по модулю 2^(8n).
		one := make([]byte, n)
		one[n-1] = 1
		got := make([]byte, n)
		counterAt(got, bytes.Repeat([]byte{0xFF}, n), one, 1)
		if !bytes.Equal(got, make([]byte, n)) {
			t.Fatalf("n=%d: переполнение дало %x", n, got)
		}
	}
}

func TestCTR_GammaFormula(t *testing.T) {
	for _, blockBits := range []int{128} {
		size := blockBits / 8
		iv := bytes.Repeat([]byte{0xFF}, size)
		c, err := New(newStub(t), testKey, modes.CTR, modes.PKCS7, WithIV(iv))
		if err != nil {
			t.Fatal(err)
		}
		gamma, err := c.Encrypt(make([]byte, size*3))
		if err != nil {
			t.Fatal(err)
		}
		d := newStub(t)
		if err := d.SetKey(testKey); err != nil {
			t.Fatal(err)
		}
		one := make([]byte, size)
		one[size-1] = 1
		for i := 0; i < 3; i++ {
			want, _ := d.EncryptBlock(refCounter(iv, one, uint64(i)))
			if !bytes.Equal(gamma[i*size:(i+1)*size], want) {
				t.Fatalf("блок %d, номер %d: гамма не равна E(IV + i)", blockBits, i)
			}
		}
	}
}

func TestRandomDelta_Scheme(t *testing.T) {
	for _, blockBits := range []int{128} {
		size := blockBits / 8
		half := size / 2
		iv := make([]byte, size)
		for i := range iv {
			iv[i] = byte(i + 1)
		}
		c, err := New(newStub(t), testKey, modes.RandomDelta, modes.PKCS7, WithIV(iv))
		if err != nil {
			t.Fatal(err)
		}
		// IV = nonce ‖ delta_iv; ctr₀ = nonce ‖ 0…0; Δ = 0…0 ‖ delta_iv с единицей в младшем бите.
		wantDelta := make([]byte, size)
		copy(wantDelta[half:], iv[half:])
		wantDelta[size-1] |= 1
		if !bytes.Equal(c.Delta(), wantDelta) {
			t.Fatalf("блок %d: Delta() = %x, ожидалось %x", blockBits, c.Delta(), wantDelta)
		}
		ctr0 := make([]byte, size)
		copy(ctr0, iv[:half])

		gamma, err := c.Encrypt(make([]byte, size*4))
		if err != nil {
			t.Fatal(err)
		}
		d := newStub(t)
		if err := d.SetKey(testKey); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 4; i++ {
			want, _ := d.EncryptBlock(refCounter(ctr0, wantDelta, uint64(i)))
			if !bytes.Equal(gamma[i*size:(i+1)*size], want) {
				t.Fatalf("блок %d, номер %d: гамма не соответствует схеме", blockBits, i)
			}
		}

		// Явная дельта становится нечётной и даёт другую гамму.
		explicit := make([]byte, size)
		explicit[size-1] = 0xA0
		e, err := New(newStub(t), testKey, modes.RandomDelta, modes.PKCS7, WithIV(iv), WithDelta(explicit))
		if err != nil {
			t.Fatal(err)
		}
		if got := e.Delta(); got[size-1] != 0xA1 {
			t.Fatalf("блок %d: WithDelta: Delta() = %x", blockBits, got)
		}
		gamma2, _ := e.Encrypt(make([]byte, size*2))
		if bytes.Equal(gamma2, gamma[:size*2]) {
			t.Fatalf("блок %d: разные дельты дали одинаковую гамму", blockBits)
		}
		msg := []byte("контекст с той же дельтой расшифровывает данные")
		ct, _ := e.Encrypt(msg)
		other, err := New(newStub(t), testKey, modes.RandomDelta, modes.PKCS7, WithIV(iv), WithDelta(e.Delta()))
		if err != nil {
			t.Fatal(err)
		}
		if pt, err := other.Decrypt(ct); err != nil || !bytes.Equal(pt, msg) {
			t.Fatalf("блок %d: расшифрование новым контекстом: %q, %v", blockBits, pt, err)
		}
	}
}

func TestStreamModes_SameKeyIVSameGamma(t *testing.T) {
	iv := randomBytes(t, 16)
	for _, mode := range []modes.CipherMode{modes.CFB, modes.OFB, modes.CTR, modes.RandomDelta} {
		a := newCtx(t, mode, modes.PKCS7, WithIV(iv))
		b := newCtx(t, mode, modes.PKCS7, WithIV(iv))
		p1, p2 := randomBytes(t, 20), randomBytes(t, 20)
		c1, _ := a.Encrypt(p1)
		c2, _ := b.Encrypt(p2)
		for i := 0; i < 16; i++ {
			if c1[i]^c2[i] != p1[i]^p2[i] {
				t.Fatalf("%s: гамма первого блока различается при одном ключе и IV", mode)
			}
		}
	}
}
