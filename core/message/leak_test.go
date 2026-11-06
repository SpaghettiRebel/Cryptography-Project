package message

import (
	"bytes"
	"crypto/rand"
	"testing"

	"cryptochat/core/cipher"
	"cryptochat/core/modes"
)

func TestOpenFieldsLeak(t *testing.T) {
	name := []byte("секретное-имя-файла.png")
	typ := []byte("image/secret-type")
	marker := bytes.Repeat([]byte("КОНТРОЛЬНЫЕ-ДАННЫЕ!"), 1000)
	for _, mode := range modes.AllCipherModes {
		for _, pad := range modes.AllPaddingModes {
			p := testParams(t, mode, pad)
			frags := sealAll(t, p, Meta{Name: string(name), Type: string(typ), TotalSize: int64(len(marker))}, marker)
			for _, f := range frags {
				wire, err := f.Marshal()
				if err != nil {
					t.Fatal(err)
				}
				for what, needle := range map[string][]byte{"имя": name, "тип": typ, "данные": marker[:40]} {
					if bytes.Contains(wire, needle) {
						t.Errorf("%s/%s: %s есть в сериализованном фрагменте", mode, pad, what)
					}
				}
			}
		}
	}
}

// Длина шифротекста не должна раскрывать размер точнее блока, в том числе без набивки (research R7).
func TestLengthHiddenWithinBlock(t *testing.T) {
	for _, mode := range modes.AllCipherModes {
		for _, pad := range modes.AllPaddingModes {
			p := testParams(t, mode, pad)
			lengths := map[int]bool{}
			for size := 100; size < 116; size++ {
				f := sealAll(t, p, Meta{TotalSize: int64(size)}, make([]byte, size))[0]
				if len(f.Cipher)%cipher.BlockSize != 0 {
					t.Errorf("%s/%s: длина шифротекста %d не кратна блоку", mode, pad, len(f.Cipher))
				}
				lengths[len(f.Cipher)] = true
			}
			if len(lengths) > 2 {
				t.Errorf("%s/%s: 16 размеров подряд дали %d разных длин", mode, pad, len(lengths))
			}
		}
	}
}

func TestFillRules(t *testing.T) {
	for dataLen := 0; dataLen < 64; dataLen++ {
		in := inner{name: "n", data: make([]byte, dataLen)}
		// Источник из одних нулей: последний байт всё равно должен стать ненулевым.
		b, err := in.build(bytes.NewReader(make([]byte, 64)))
		if err != nil {
			t.Fatal(err)
		}
		if len(b)%cipher.BlockSize != 0 {
			t.Fatalf("длина %d не кратна блоку", len(b))
		}
		fill := len(b) - in.headerSize() - dataLen
		if fill < 1 || fill > cipher.BlockSize {
			t.Fatalf("заполнение %d байт", fill)
		}
		if b[len(b)-1] == 0 {
			t.Fatal("последний байт должен быть ненулевым")
		}
		if _, err := parseInner(b); err != nil {
			t.Fatalf("разбор: %v", err)
		}
	}
}

func TestParseInnerNoPanic(t *testing.T) {
	in := inner{name: "имя", typ: "тип", data: []byte("данные")}
	valid, err := in.build(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= len(valid); i++ {
		_, _ = parseInner(valid[:i])
	}
	r := detReader{state: 42}
	for i := 0; i < 20000; i++ {
		b := append([]byte(nil), valid...)
		noise := make([]byte, 4)
		_, _ = r.Read(noise)
		b[int(noise[0])%len(b)] ^= noise[1]
		b[int(noise[2])%len(b)] ^= noise[3]
		_, _ = parseInner(b)
	}
}

func TestDifferentIVs(t *testing.T) {
	p := testParams(t, modes.CBC, modes.PKCS7)
	frags := sealAll(t, p, Meta{TotalSize: 3 * ChunkSize}, make([]byte, 3*ChunkSize))
	seen := map[string]bool{}
	for _, f := range frags {
		if seen[string(f.IV)] {
			t.Fatal("IV повторился")
		}
		seen[string(f.IV)] = true
	}
	ecb := sealAll(t, testParams(t, modes.ECB, modes.PKCS7), Meta{TotalSize: 10}, make([]byte, 10))
	if len(ecb[0].IV) != 0 {
		t.Fatal("в ECB IV не нужен")
	}
}
