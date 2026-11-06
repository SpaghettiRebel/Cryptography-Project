package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func sample() *Fragment {
	return &Fragment{
		ChatID:    [IDSize]byte{1, 2, 3},
		MessageID: [IDSize]byte{4, 5, 6},
		Index:     2,
		Total:     5,
		IV:        bytes.Repeat([]byte{0xAA}, IVSize),
		Cipher:    bytes.Repeat([]byte{0xBB}, 32),
	}
}

func TestRoundTrip(t *testing.T) {
	for name, f := range map[string]*Fragment{
		"с IV": sample(),
		"ECB":  func() *Fragment { x := sample(); x.IV = nil; return x }(),
		"максимум": func() *Fragment {
			x := sample()
			x.Index, x.Total = MaxTotal-1, MaxTotal
			x.Cipher = make([]byte, MaxCipherLen)
			return x
		}(),
	} {
		data, err := f.Marshal()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := Unmarshal(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.ChatID != f.ChatID || got.MessageID != f.MessageID || got.Index != f.Index || got.Total != f.Total ||
			!bytes.Equal(got.IV, f.IV) || !bytes.Equal(got.Cipher, f.Cipher) {
			t.Fatalf("%s: результат отличается", name)
		}
	}
}

func TestUnmarshalDoesNotKeepInput(t *testing.T) {
	data, err := sample().Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	for i := range data {
		data[i] = 0
	}
	if got.Cipher[0] != 0xBB || got.IV[0] != 0xAA {
		t.Fatal("результат не должен ссылаться на входной срез")
	}
}

func TestMarshalValidates(t *testing.T) {
	cases := map[string]func(*Fragment){
		"Index = Total": func(f *Fragment) { f.Index = f.Total },
		"Total = 0":     func(f *Fragment) { f.Total, f.Index = 0, 0 },
		"Total велик":   func(f *Fragment) { f.Total = MaxTotal + 1 },
		"IV 15 байт":    func(f *Fragment) { f.IV = make([]byte, 15) },
		"пустой шифр":   func(f *Fragment) { f.Cipher = nil },
		"шифр велик":    func(f *Fragment) { f.Cipher = make([]byte, MaxCipherLen+1) },
	}
	for name, mutate := range cases {
		f := sample()
		mutate(f)
		if _, err := f.Marshal(); !errors.Is(err, ErrMalformedFragment) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBoundaries(t *testing.T) {
	valid, err := sample().Marshal()
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(fn func(b []byte) []byte) []byte { return fn(append([]byte(nil), valid...)) }
	const offsetIndex = 2 + 1 + IDSize*2
	cases := map[string][]byte{
		"пусто":          {},
		"неверный Magic": mutate(func(b []byte) []byte { b[0] = 'X'; return b }),
		"Index = Total": mutate(func(b []byte) []byte {
			binary.BigEndian.PutUint32(b[offsetIndex:], 5)
			return b
		}),
		"Total = 0": mutate(func(b []byte) []byte {
			binary.BigEndian.PutUint32(b[offsetIndex:], 0)
			binary.BigEndian.PutUint32(b[offsetIndex+4:], 0)
			return b
		}),
		"Total велик": mutate(func(b []byte) []byte {
			binary.BigEndian.PutUint32(b[offsetIndex+4:], MaxTotal+1)
			return b
		}),
		"IVLen = 7":    mutate(func(b []byte) []byte { b[offsetIndex+8] = 7; return b }),
		"усечение":     valid[:len(valid)-1],
		"лишние байты": append(append([]byte(nil), valid...), 0),
		"CipherLen = 0": mutate(func(b []byte) []byte {
			binary.BigEndian.PutUint32(b[offsetIndex+9+IVSize:], 0)
			return b
		}),
		"CipherLen велик": mutate(func(b []byte) []byte {
			binary.BigEndian.PutUint32(b[offsetIndex+9+IVSize:], MaxCipherLen+1)
			return b
		}),
		"заявлено больше данных": mutate(func(b []byte) []byte {
			binary.BigEndian.PutUint32(b[offsetIndex+9+IVSize:], MaxCipherLen)
			return b
		}),
	}
	for name, data := range cases {
		if _, err := Unmarshal(data); !errors.Is(err, ErrMalformedFragment) {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad := mutate(func(b []byte) []byte { b[2] = 9; return b })
	if _, err := Unmarshal(bad); !errors.Is(err, ErrUnsupportedVersion) {
		t.Errorf("версия: %v", err)
	}
}

// Заявленный огромный размер не должен приводить к выделению памяти под него.
func TestOversizedClaimDoesNotAllocate(t *testing.T) {
	valid, err := sample().Marshal()
	if err != nil {
		t.Fatal(err)
	}
	const off = 2 + 1 + IDSize*2 + 9 + IVSize
	binary.BigEndian.PutUint32(valid[off:], MaxCipherLen)
	allocs := testing.AllocsPerRun(100, func() {
		if _, err := Unmarshal(valid); err == nil {
			t.Fatal("должна быть ошибка")
		}
	})
	if allocs > 2 {
		t.Fatalf("выделений памяти: %v", allocs)
	}
}

// xorshift — детерминированный источник для перебора повреждений.
type xorshift uint64

func (x *xorshift) next() uint64 {
	v := uint64(*x)
	v ^= v << 13
	v ^= v >> 7
	v ^= v << 17
	*x = xorshift(v)
	return v
}

func TestCorruptedInputs(t *testing.T) {
	valid, err := sample().Marshal()
	if err != nil {
		t.Fatal(err)
	}
	r := xorshift(0x1234567890ABCDEF)
	const variants = 100_000
	for i := 0; i < variants; i++ {
		data := append([]byte(nil), valid...)
		switch r.next() % 3 {
		case 0:
			for n := r.next()%8 + 1; n > 0; n-- {
				data[r.next()%uint64(len(data))] ^= byte(r.next())
			}
		case 1:
			data = data[:r.next()%uint64(len(data))]
		default:
			extra := make([]byte, r.next()%32)
			for j := range extra {
				extra[j] = byte(r.next())
			}
			data = append(data[:r.next()%uint64(len(data))], extra...)
		}
		_, _ = Unmarshal(data)
	}
}

func FuzzUnmarshal(f *testing.F) {
	valid, err := sample().Marshal()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte{})
	f.Add([]byte("CF\x01"))
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := Unmarshal(data)
		if err != nil {
			return
		}
		again, err := got.Marshal()
		if err != nil {
			t.Fatalf("разобранный фрагмент не сериализуется: %v", err)
		}
		if !bytes.Equal(again, data) {
			t.Fatal("сериализация не обратна разбору")
		}
	})
}
