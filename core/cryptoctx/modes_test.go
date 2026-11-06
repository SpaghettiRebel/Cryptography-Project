package cryptoctx

import (
	"bytes"
	"fmt"
	"sync"
	"testing"

	"cryptochat/core/modes"
)

// У PCBC опубликованных векторов нет, поэтому схема проверяется по определению:
// первый блок совпадает с CBC, а следующий равен E(Pᵢ ⊕ Pᵢ₋₁ ⊕ Cᵢ₋₁).
func TestPCBC_Structure(t *testing.T) {
	iv := randomBytes(t, 16)
	pt := randomBytes(t, 32)
	c := newCtx(t, modes.PCBC, modes.PKCS7, WithIV(iv))
	ct, err := c.Encrypt(pt)
	if err != nil {
		t.Fatal(err)
	}
	cbc, err := newCtx(t, modes.CBC, modes.PKCS7, WithIV(iv)).Encrypt(pt)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ct[:16], cbc[:16]) {
		t.Fatal("первый блок PCBC должен совпасть с первым блоком CBC")
	}
	if bytes.Equal(ct[16:32], cbc[16:32]) {
		t.Fatal("второй блок PCBC должен отличаться от CBC")
	}
	d := newStub(t)
	if err := d.SetKey(testKey); err != nil {
		t.Fatal(err)
	}
	in := make([]byte, 16)
	xorInto(in, pt[16:32], pt[:16])
	xorInto(in, in, ct[:16])
	want, _ := d.EncryptBlock(in)
	if !bytes.Equal(ct[16:32], want) {
		t.Fatalf("C₁ = %x, ожидалось %x", ct[16:32], want)
	}
}

func TestModes_ParallelMatchesSequential(t *testing.T) {
	for _, blockBits := range []int{128} {
		size := blockBits / 8
		data := randomBytes(t, size*1000+5)
		iv := randomBytes(t, size)
		for _, mode := range modes.AllCipherModes {
			make2 := func(workers int) *CryptoContext {
				opts := append(ivOption(mode, iv), WithWorkers(workers))
				c, err := New(newStub(t), testKey, mode, modes.PKCS7, opts...)
				if err != nil {
					t.Fatal(err)
				}
				return c
			}
			seq, par := make2(1), make2(7)
			ctSeq, err := seq.Encrypt(data)
			if err != nil {
				t.Fatal(err)
			}
			ctPar, err := par.Encrypt(data)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(ctSeq, ctPar) {
				t.Fatalf("блок %d, %s: параллельное шифрование отличается", blockBits, mode)
			}
			ptSeq, _ := seq.Decrypt(ctPar)
			ptPar, _ := par.Decrypt(ctSeq)
			if !bytes.Equal(ptSeq, data) || !bytes.Equal(ptPar, data) {
				t.Fatalf("блок %d, %s: расшифрование отличается", blockBits, mode)
			}
		}
	}
}

// Как портится текст при одном испорченном бите шифротекста: это определение режимов.
func TestModes_ErrorPropagation(t *testing.T) {
	const blocks = 6
	data := randomBytes(t, 16*blocks)
	data[len(data)-1] |= 1
	for _, mode := range modes.AllCipherModes {
		c := newCtx(t, mode, modes.Zeros)
		ct, err := c.Encrypt(data)
		if err != nil {
			t.Fatal(err)
		}
		ct[2*16+3] ^= 0x01
		pt, err := c.Decrypt(ct)
		if err != nil {
			t.Fatalf("%s: расшифрование после порчи: %v", mode, err)
		}
		var corrupted []int
		for i := 0; i < blocks; i++ {
			if !bytes.Equal(pt[i*16:(i+1)*16], data[i*16:(i+1)*16]) {
				corrupted = append(corrupted, i)
			}
		}
		var want []int
		switch mode {
		case modes.ECB, modes.OFB, modes.CTR, modes.RandomDelta:
			want = []int{2}
		case modes.CBC, modes.CFB:
			want = []int{2, 3}
		case modes.PCBC:
			want = []int{2, 3, 4, 5}
		}
		if fmt.Sprint(corrupted) != fmt.Sprint(want) {
			t.Errorf("%s: испорчены блоки %v, ожидалось %v", mode, corrupted, want)
		}
	}
}

func TestContext_ConcurrentUse(t *testing.T) {
	c := newCtx(t, modes.CBC, modes.PKCS7)
	data := randomBytes(t, 16*64+3)
	want, err := c.Encrypt(data)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ct, err := c.Encrypt(data)
			if err != nil || !bytes.Equal(ct, want) {
				errs <- fmt.Errorf("параллельное шифрование отличается: %v", err)
				return
			}
			pt, err := c.Decrypt(ct)
			if err != nil || !bytes.Equal(pt, data) {
				errs <- fmt.Errorf("параллельное расшифрование отличается: %v", err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
