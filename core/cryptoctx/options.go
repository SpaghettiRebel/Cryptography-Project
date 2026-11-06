package cryptoctx

import (
	"fmt"

	"cryptochat/core/modes"
)

type Option func(*CryptoContext) error

func WithIV(iv []byte) Option {
	return func(c *CryptoContext) error {
		if len(iv) == 0 {
			return fmt.Errorf("cryptoctx: WithIV: empty IV")
		}
		c.iv = append([]byte(nil), iv...)
		return nil
	}
}

// WithWorkers задаёт число горутин.
func WithWorkers(n int) Option {
	return func(c *CryptoContext) error {
		if n < 1 {
			return fmt.Errorf("cryptoctx: WithWorkers: need at least 1 worker, got %d", n)
		}
		c.workers = n
		return nil
	}
}

// WithDelta — дополнительный параметр режима RandomDelta.
func WithDelta(delta []byte) Option {
	return func(c *CryptoContext) error {
		if c.mode != modes.RandomDelta {
			return fmt.Errorf("cryptoctx: WithDelta is only valid for %s mode, not %s", modes.RandomDelta, c.mode)
		}
		if len(delta) == 0 {
			return fmt.Errorf("cryptoctx: WithDelta: empty delta")
		}
		c.delta = append([]byte(nil), delta...)
		return nil
	}
}
