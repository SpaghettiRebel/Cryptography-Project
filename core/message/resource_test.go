package message

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cryptochat/core/internal/stubcipher"
	"cryptochat/core/modes"
	"cryptochat/core/protocol"
)

func TestMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("100 МБ")
	}
	const size = 100 * 1000 * 1000
	defer debug.SetGCPercent(debug.SetGCPercent(10))

	p := testParams(t, modes.CBC, modes.PKCS7)
	var live, peak atomic.Int64
	cfg := testConfig(t, p)
	cfg.workers = maxWorkers
	cfg.track = func(delta int) {
		v := live.Add(int64(delta))
		for {
			old := peak.Load()
			if v <= old || peak.CompareAndSwap(old, v) {
				return
			}
		}
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	stop := make(chan struct{})
	var heapPeak atomic.Uint64
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > heapPeak.Load() {
					heapPeak.Store(m.HeapAlloc)
				}
			}
		}
	}()

	a := newTestAssembler2(t, p)
	err := seal(context.Background(), cfg, p, testMessageID, Meta{TotalSize: size}, &patternReader{left: size},
		func(f *protocol.Fragment) error { return a.Add(context.Background(), f) }, nil)
	close(stop)
	sampler.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Finish(); err != nil {
		t.Fatal(err)
	}
	if live.Load() != 0 {
		t.Fatalf("учёт буферов не сошёлся: %d", live.Load())
	}
	if peak.Load() > 16 {
		t.Fatalf("в пике %d объёмов фрагментов, допустимо 16", peak.Load())
	}
	// Замер кучи шумит из-за самой среды выполнения и сборщика, поэтому главный критерий — счётчик выше.
	// Запас поверх 16 МиБ покрывает мусор, ещё не убранный сборщиком, и расшифрование в Assembler.
	const heapLimit = 64 << 20
	growth := int64(heapPeak.Load()) - int64(before.HeapAlloc)
	t.Logf("пик счётчика %d, рост кучи %.1f МиБ", peak.Load(), float64(growth)/(1<<20))
	if growth > heapLimit {
		t.Fatalf("рост кучи %d байт больше запаса %d", growth, int64(heapLimit))
	}
}

func newTestAssembler2(t *testing.T, p Params) *Assembler {
	t.Helper()
	a, err := newAssembler(stubFor(t, p), p, testMessageID, discardWriter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestCancel(t *testing.T) {
	p := testParams(t, modes.CBC, modes.PKCS7)
	c, err := stubcipher.NewWithLoad(128, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetKey(p.Key); err != nil {
		t.Fatal(err)
	}
	cfg := sealConfig{cipher: c, rand: &detReader{state: 9}, workers: 4}
	drop := func(*protocol.Fragment) error { return nil }

	// До начала.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = seal(ctx, cfg, p, testMessageID, Meta{TotalSize: 10}, bytes.NewReader(make([]byte, 10)), drop, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("до начала: %v", err)
	}

	// В середине.
	ctx, cancel = context.WithCancel(context.Background())
	const size = 64 * ChunkSize
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err = seal(ctx, cfg, p, testMessageID, Meta{TotalSize: size}, &patternReader{left: size}, drop, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("в середине: %v", err)
	}
	if d := time.Since(start) - 50*time.Millisecond; d > time.Second {
		t.Errorf("остановка заняла %v", d)
	}

	// Сразу после завершения результат остаётся успешным.
	ctx, cancel = context.WithCancel(context.Background())
	err = seal(ctx, cfg, p, testMessageID, Meta{TotalSize: 10}, bytes.NewReader(make([]byte, 10)), drop, nil)
	cancel()
	if err != nil {
		t.Errorf("после завершения: %v", err)
	}

	// Сборка.
	frags := sealAll(t, p, Meta{TotalSize: 10}, make([]byte, 10))
	a := newTestAssembler2(t, p)
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if err := a.Add(ctx, frags[0]); !errors.Is(err, context.Canceled) {
		t.Errorf("Add: %v", err)
	}
}

func TestProgress(t *testing.T) {
	check := func(t *testing.T, size int64) {
		p := testParams(t, modes.CTR, modes.PKCS7)
		var mu sync.Mutex
		var seen []int64
		active := false
		record := func(done, total int64) {
			mu.Lock()
			defer mu.Unlock()
			if active {
				t.Error("вызовы пересекаются")
			}
			active = true
			if total != size {
				t.Errorf("total %d", total)
			}
			seen = append(seen, done)
			active = false
		}
		var frags []*protocol.Fragment
		err := seal(context.Background(), testConfig(t, p), p, testMessageID, Meta{TotalSize: size}, &patternReader{left: size},
			func(f *protocol.Fragment) error {
				frags = append(frags, f)
				return nil
			}, record)
		if err != nil {
			t.Fatal(err)
		}
		verify := func(what string) {
			if len(seen) == 0 || seen[len(seen)-1] != size {
				t.Fatalf("%s: прогресс %v, конец должен быть %d", what, seen, size)
			}
			for i := 1; i < len(seen); i++ {
				if seen[i] < seen[i-1] {
					t.Fatalf("%s: прогресс убывает: %v", what, seen)
				}
			}
		}
		verify("seal")

		seen = nil
		a, err := newAssembler(stubFor(t, p), p, testMessageID, discardWriter{}, record)
		if err != nil {
			t.Fatal(err)
		}
		for i := len(frags) - 1; i >= 0; i-- {
			if err := a.Add(context.Background(), frags[i]); err != nil {
				t.Fatal(err)
			}
		}
		verify("assembler")
	}
	for _, size := range []int64{0, 1, ChunkSize, 3*ChunkSize + 5} {
		check(t, size)
	}
}
