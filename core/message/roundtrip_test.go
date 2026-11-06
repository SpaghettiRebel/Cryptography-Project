package message

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	mathbits "math/bits"
	"testing"

	"cryptochat/core/modes"
	"cryptochat/core/protocol"
)

var ioEOF = io.EOF

// shuffle — детерминированная перестановка, без предсказуемого генератора из math/rand.
func shuffle(fs []*protocol.Fragment) {
	state := uint64(0xDEADBEEF12345)
	for i := len(fs) - 1; i > 0; i-- {
		state = mathbits.RotateLeft64(state*0x9E3779B97F4A7C15, 17)
		j := int(state % uint64(i+1))
		fs[i], fs[j] = fs[j], fs[i]
	}
}

func roundTrip(t *testing.T, p Params, meta Meta, data []byte) {
	t.Helper()
	frags := sealAll(t, p, meta, data)
	if want := fragmentCount(meta.TotalSize); int64(len(frags)) != want {
		t.Fatalf("фрагментов %d, ожидалось %d", len(frags), want)
	}
	shuffle(frags)
	out := &memWriter{}
	a := newTestAssembler(t, p, out)
	for _, f := range frags {
		wire, err := f.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := protocol.Unmarshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Add(context.Background(), parsed); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	got, err := a.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if got != meta {
		t.Fatalf("метаданные %+v, ожидалось %+v", got, meta)
	}
	if !bytes.Equal(out.data, data) && (len(out.data) != 0 || len(data) != 0) {
		t.Fatalf("данные не совпали: длины %d и %d", len(out.data), len(data))
	}
}

func TestRoundTrip(t *testing.T) {
	sizes := []int{0, 1, 15, 16, 17}
	if !testing.Short() {
		sizes = append(sizes, ChunkSize, ChunkSize+1, 2*ChunkSize+100)
	}
	for _, mode := range modes.AllCipherModes {
		for _, pad := range modes.AllPaddingModes {
			for _, size := range sizes {
				t.Run(fmt.Sprintf("%s/%s/%d", mode, pad, size), func(t *testing.T) {
					data := make([]byte, size)
					if _, err := rand.Read(data); err != nil {
						t.Fatal(err)
					}
					roundTrip(t, testParams(t, mode, pad),
						Meta{Name: "файл.bin", Type: "application/octet-stream", TotalSize: int64(size)}, data)
				})
			}
		}
	}
}

// Нули в конце сохраняются при любой набивке, потому что точная длина лежит внутри шифротекста.
func TestTrailingZerosSurvive(t *testing.T) {
	data := []byte{1, 2, 3, 0, 0, 0, 0}
	for _, mode := range modes.AllCipherModes {
		for _, pad := range modes.AllPaddingModes {
			roundTrip(t, testParams(t, mode, pad), Meta{TotalSize: int64(len(data))}, data)
			if !testing.Short() {
				zeros := make([]byte, ChunkSize+40)
				roundTrip(t, testParams(t, mode, pad), Meta{TotalSize: int64(len(zeros))}, zeros)
			}
		}
	}
}

func TestRoundTripLarge(t *testing.T) {
	if testing.Short() {
		t.Skip("100 МБ при всех режимах")
	}
	const size = 100 * 1000 * 1000
	for _, mode := range modes.AllCipherModes {
		t.Run(mode.String(), func(t *testing.T) {
			p := testParams(t, mode, modes.PKCS7)
			out := &memWriter{data: make([]byte, 0, size)}
			a := newTestAssembler(t, p, out)
			meta := Meta{Name: "big", TotalSize: size}
			err := seal(context.Background(), testConfig(t, p), p, testMessageID, meta, &patternReader{left: size},
				func(f *protocol.Fragment) error { return a.Add(context.Background(), f) }, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Finish(); err != nil {
				t.Fatal(err)
			}
			want := make([]byte, size)
			if _, err := io.ReadFull(&patternReader{left: size}, want); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.data, want) {
				t.Fatal("данные не совпали")
			}
		})
	}
}
