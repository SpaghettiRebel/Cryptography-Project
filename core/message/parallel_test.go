package message

import (
	"bytes"
	"context"
	"runtime"
	"testing"
	"time"

	"cryptochat/core/internal/stubcipher"
	"cryptochat/core/modes"
	"cryptochat/core/protocol"
)

func sealWith(t *testing.T, p Params, workers int, size int64) []*protocol.Fragment {
	t.Helper()
	cfg := testConfig(t, p)
	cfg.workers = workers
	cfg.rand = &detReader{state: 0x1122334455667788}
	var out []*protocol.Fragment
	next := uint32(0)
	err := seal(context.Background(), cfg, p, testMessageID, Meta{Name: "n", TotalSize: size}, &patternReader{left: size},
		func(f *protocol.Fragment) error {
			if f.Index != next {
				t.Errorf("фрагмент %d вместо %d", f.Index, next)
			}
			next++
			out = append(out, f)
			return nil
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestParallel(t *testing.T) {
	const size = 9*ChunkSize + 123
	for _, mode := range modes.AllCipherModes {
		// Здесь набивка без случайных байтов: иначе результат отличался бы и без параллельности.
		p := testParams(t, mode, modes.PKCS7)
		a := sealWith(t, p, 1, size)
		b := sealWith(t, p, 8, size)
		if len(a) != len(b) {
			t.Fatalf("%s: разное число фрагментов", mode)
		}
		for i := range a {
			if !bytes.Equal(a[i].IV, b[i].IV) || !bytes.Equal(a[i].Cipher, b[i].Cipher) {
				t.Fatalf("%s: фрагмент %d различается", mode, i)
			}
		}
	}
}

func TestParallelSpeedup(t *testing.T) {
	if testing.Short() {
		t.Skip("замер времени")
	}
	if runtime.GOMAXPROCS(0) < 4 {
		t.Skipf("нужно 4 и более ядер, доступно %d", runtime.GOMAXPROCS(0))
	}
	p := testParams(t, modes.CTR, modes.PKCS7)
	c, err := stubcipher.NewWithLoad(128, 400)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetKey(p.Key); err != nil {
		t.Fatal(err)
	}
	const size = 20 * ChunkSize
	measure := func(workers int) time.Duration {
		cfg := sealConfig{cipher: c, rand: &detReader{state: 7}, workers: workers}
		start := time.Now()
		err := seal(context.Background(), cfg, p, testMessageID, Meta{TotalSize: size}, &patternReader{left: size},
			func(*protocol.Fragment) error { return nil }, nil)
		if err != nil {
			t.Fatal(err)
		}
		return time.Since(start)
	}
	seq, par := measure(1), measure(defaultWorkers())
	t.Logf("последовательно %v, параллельно %v (%d рабочих), ускорение %.2f", seq, par, defaultWorkers(), float64(seq)/float64(par))
	if float64(seq)/float64(par) < 2 {
		t.Fatalf("ускорение %.2f меньше 2", float64(seq)/float64(par))
	}
}
