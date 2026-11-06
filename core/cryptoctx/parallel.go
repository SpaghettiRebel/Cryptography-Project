package cryptoctx

import (
	"context"
	"sync"
)

// cancelCheckEvery — как часто (в блоках) проверять отмену контекста
const cancelCheckEvery = 64

func forEachBlock(ctx context.Context, n, workers int, fn func(i int) error) error {
	if n <= 0 {
		return ctx.Err()
	}
	if workers > n {
		workers = n
	}
	if workers <= 1 {
		return forEachBlockSeq(ctx, n, fn)
	}

	parent := ctx
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	fail := func(err error) {
		once.Do(func() {
			firstErr = err
			cancel()
		})
	}
	chunk := (n + workers - 1) / workers
	for start := 0; start < n; start += chunk {
		end := min(start+chunk, n)
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			for i := start; i < end; i++ {
				if (i-start)%cancelCheckEvery == 0 && ctx.Err() != nil {
					return
				}
				if err := fn(i); err != nil {
					fail(err)
					return
				}
			}
		}(start, end)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return parent.Err()
}

// forEachBlockSeq — последовательный вариант с той же поддержкой отмены
func forEachBlockSeq(ctx context.Context, n int, fn func(i int) error) error {
	for i := 0; i < n; i++ {
		if i%cancelCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if err := fn(i); err != nil {
			return err
		}
	}
	return nil
}

func xorInto(dst, a, b []byte) {
	for i := range dst {
		dst[i] = a[i] ^ b[i]
	}
}
