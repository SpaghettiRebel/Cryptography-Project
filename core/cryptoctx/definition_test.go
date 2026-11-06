package cryptoctx

import (
	"bytes"
	"testing"

	"cryptochat/core/internal/stubcipher"
	"cryptochat/core/modes"
)

// Шифротекст каждого режима считается здесь отдельным кодом по определению режима, без пакетов
// cryptoctx и padding. Random Delta — по определению из исследование решений.

const defBS = 16

func defXor(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

// defAdd — сложение (x + y·i) по модулю 2^128, big-endian.
func defAdd(x, y []byte, i int) []byte {
	out := append([]byte(nil), x...)
	for n := 0; n < i; n++ {
		carry := 0
		for b := defBS - 1; b >= 0; b-- {
			v := int(out[b]) + int(y[b]) + carry
			out[b] = byte(v)
			carry = v >> 8
		}
	}
	return out
}

func TestModesByDefinition(t *testing.T) {
	key := unhex("000102030405060708090a0b0c0d0e0f")
	iv := unhex("a0a1a2a3a4a5a6a7b0b1b2b3b4b5b6b7")
	pt := make([]byte, 4*defBS)
	for i := range pt {
		pt[i] = byte(i*7 + 1)
	}
	e, err := stubcipher.New(128)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.SetKey(key); err != nil {
		t.Fatal(err)
	}
	enc := func(b []byte) []byte {
		out, err := e.EncryptBlock(b)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	block := func(i int) []byte { return pt[i*defBS : (i+1)*defBS] }

	want := map[modes.CipherMode][]byte{}
	var ecb, cbc, pcbc, cfb, ofb, ctr, rd []byte
	prevC, pcC, pcP, cfbC, gamma := iv, iv, make([]byte, defBS), iv, iv

	nonceCtr := make([]byte, defBS)
	copy(nonceCtr, iv[:defBS/2])
	delta := make([]byte, defBS)
	copy(delta[defBS/2:], iv[defBS/2:])
	delta[defBS-1] |= 1
	one := make([]byte, defBS)
	one[defBS-1] = 1

	for i := 0; i < 4; i++ {
		p := block(i)
		ecb = append(ecb, enc(p)...)

		c := enc(defXor(p, prevC))
		cbc = append(cbc, c...)
		prevC = c

		pc := enc(defXor(defXor(p, pcP), pcC))
		pcbc = append(pcbc, pc...)
		pcP, pcC = p, pc

		fc := defXor(p, enc(cfbC))
		cfb = append(cfb, fc...)
		cfbC = fc

		gamma = enc(gamma)
		ofb = append(ofb, defXor(p, gamma)...)

		ctr = append(ctr, defXor(p, enc(defAdd(iv, one, i)))...)
		rd = append(rd, defXor(p, enc(defAdd(nonceCtr, delta, i)))...)
	}
	want[modes.ECB], want[modes.CBC], want[modes.PCBC] = ecb, cbc, pcbc
	want[modes.CFB], want[modes.OFB], want[modes.CTR], want[modes.RandomDelta] = cfb, ofb, ctr, rd

	for _, mode := range modes.AllCipherModes {
		t.Run(mode.String(), func(t *testing.T) {
			c, err := stubcipher.New(128)
			if err != nil {
				t.Fatal(err)
			}
			ctx, err := New(c, key, mode, modes.PKCS7, ivOption(mode, iv)...)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ctx.Encrypt(pt)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got[:4*defBS], want[mode]) {
				t.Fatalf("шифротекст\n%x\nпо определению\n%x", got[:4*defBS], want[mode])
			}
			back, err := ctx.Decrypt(got)
			if err != nil || !bytes.Equal(back, pt) {
				t.Fatalf("расшифрование: %x, %v", back, err)
			}
		})
	}
}

// Явная дельта заменяет вторую половину IV; младший бит устанавливается и у неё.
func TestRandomDeltaExplicitByDefinition(t *testing.T) {
	key := unhex("000102030405060708090a0b0c0d0e0f")
	iv := unhex("a0a1a2a3a4a5a6a7b0b1b2b3b4b5b6b7")
	explicit := unhex("00000000000000000000000000000102")
	pt := bytes.Repeat([]byte{0x42}, 2*defBS)

	c, err := stubcipher.New(128)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := New(c, key, modes.RandomDelta, modes.PKCS7, WithIV(iv), WithDelta(explicit))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctx.Encrypt(pt)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := stubcipher.New(128)
	if err := e.SetKey(key); err != nil {
		t.Fatal(err)
	}
	ctr0 := make([]byte, defBS)
	copy(ctr0, iv[:defBS/2])
	delta := append([]byte(nil), explicit...)
	delta[defBS-1] |= 1
	var want []byte
	for i := 0; i < 2; i++ {
		g, err := e.EncryptBlock(defAdd(ctr0, delta, i))
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, defXor(pt[i*defBS:(i+1)*defBS], g)...)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("шифротекст %x, по определению %x", got, want)
	}
}
