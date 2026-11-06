package cryptoctx

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"runtime"

	"cryptochat/core/cipher"
	"cryptochat/core/modes"
)

type CryptoContext struct {
	cipher    cipher.BlockCipher
	mode      modes.CipherMode
	padding   modes.PaddingMode
	blockSize int
	iv        []byte // nil только для ECB
	delta     []byte // RandomDelta: шаг счётчика (blockSize байт, нечётный)
	ctr0      []byte // CTR/RandomDelta: начальное значение счётчика
	workers   int
}

type AsyncResult struct {
	Data []byte
	Err  error
}

// New вызывает SetKey у переданного шифра, поэтому контекст владеет им: один объект шифра нельзя
// делить между контекстами с разными ключами.
func New(c cipher.BlockCipher, key []byte, mode modes.CipherMode, pad modes.PaddingMode, opts ...Option) (*CryptoContext, error) {
	if err := validate(c, mode, pad); err != nil {
		return nil, err
	}
	if err := c.SetKey(key); err != nil {
		return nil, fmt.Errorf("cryptoctx: set key: %w", err)
	}
	return build(c, mode, pad, opts)
}

// NewWithCipher принимает шифр с уже заданным ключом и не вызывает SetKey, поэтому один шифр можно
// использовать во многих контекстах одновременно.
func NewWithCipher(keyed cipher.BlockCipher, mode modes.CipherMode, pad modes.PaddingMode, opts ...Option) (*CryptoContext, error) {
	if err := validate(keyed, mode, pad); err != nil {
		return nil, err
	}
	return build(keyed, mode, pad, opts)
}

func validate(c cipher.BlockCipher, mode modes.CipherMode, pad modes.PaddingMode) error {
	if c == nil {
		return fmt.Errorf("cryptoctx: block cipher must not be nil")
	}
	if !mode.IsValid() {
		return fmt.Errorf("cryptoctx: unknown cipher mode %s", mode)
	}
	if !pad.IsValid() {
		return fmt.Errorf("cryptoctx: unknown padding mode %s", pad)
	}
	if bs := c.BlockSize(); bs <= 0 || bs%2 != 0 || bs > 255 {
		return fmt.Errorf("cryptoctx: unsupported block size %d", bs)
	}
	return nil
}

// checkedCipher не даёт реализации с неверной длиной результата молча испортить соседний блок.
type checkedCipher struct {
	cipher.BlockCipher
}

func (k checkedCipher) checked(out []byte, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	if len(out) != k.BlockSize() {
		return nil, cipher.ErrInvalidBlockSize
	}
	return out, nil
}

func (k checkedCipher) EncryptBlock(block []byte) ([]byte, error) {
	return k.checked(k.BlockCipher.EncryptBlock(block))
}

func (k checkedCipher) DecryptBlock(block []byte) ([]byte, error) {
	return k.checked(k.BlockCipher.DecryptBlock(block))
}

func build(bc cipher.BlockCipher, mode modes.CipherMode, pad modes.PaddingMode, opts []Option) (*CryptoContext, error) {
	bs := bc.BlockSize()
	c := &CryptoContext{
		cipher:    checkedCipher{bc},
		mode:      mode,
		padding:   pad,
		blockSize: bs,
		workers:   runtime.NumCPU(),
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	if err := c.finishSetup(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *CryptoContext) finishSetup() error {
	if !c.mode.NeedsIV() {
		if c.iv != nil {
			return fmt.Errorf("cryptoctx: mode %s does not use an IV", c.mode)
		}
		return nil
	}
	if c.iv == nil {
		c.iv = make([]byte, c.blockSize)
		if _, err := rand.Read(c.iv); err != nil {
			return fmt.Errorf("cryptoctx: generate IV: %w", err)
		}
	}
	if len(c.iv) != c.blockSize {
		return fmt.Errorf("cryptoctx: IV must be %d bytes (block size), got %d", c.blockSize, len(c.iv))
	}
	switch c.mode {
	case modes.CTR:
		// ctr_i = IV + i
		c.ctr0 = append([]byte(nil), c.iv...)
		c.delta = make([]byte, c.blockSize)
		c.delta[c.blockSize-1] = 1
	case modes.RandomDelta:
		// IV = nonce || delta_iv,
		// ctr_0 = nonce || 0…0, Δ = delta_iv (или WithDelta), Δ |= 1.
		half := c.blockSize / 2
		c.ctr0 = make([]byte, c.blockSize)
		copy(c.ctr0, c.iv[:half])
		if c.delta == nil {
			c.delta = make([]byte, c.blockSize)
			copy(c.delta[half:], c.iv[half:])
		} else if len(c.delta) != c.blockSize {
			return fmt.Errorf("cryptoctx: delta must be %d bytes (block size), got %d", c.blockSize, len(c.delta))
		}
		c.delta[c.blockSize-1] |= 1
	}
	return nil
}

func (c *CryptoContext) Mode() modes.CipherMode { return c.mode }

func (c *CryptoContext) Padding() modes.PaddingMode { return c.padding }

func (c *CryptoContext) BlockSize() int { return c.blockSize }

func (c *CryptoContext) Workers() int { return c.workers }

func (c *CryptoContext) IV() []byte { return append([]byte(nil), c.iv...) }

func (c *CryptoContext) Delta() []byte {
	if c.mode != modes.RandomDelta {
		return nil
	}
	return append([]byte(nil), c.delta...)
}

// Encrypt использует IV контекста: повторный вызов на том же контексте повторяет IV, а в CTR, OFB и
// Random Delta это повтор гаммы. Для каждого сообщения нужен новый контекст, как делает пакет message.
func (c *CryptoContext) Encrypt(data []byte) ([]byte, error) {
	return c.encrypt(context.Background(), data)
}

func (c *CryptoContext) Decrypt(data []byte) ([]byte, error) {
	return c.decrypt(context.Background(), data)
}

// EncryptContext — Encrypt с поддержкой отмены через ctx.
func (c *CryptoContext) EncryptContext(ctx context.Context, data []byte) ([]byte, error) {
	return c.encrypt(ctx, data)
}

// DecryptContext — Decrypt с поддержкой отмены через ctx.
func (c *CryptoContext) DecryptContext(ctx context.Context, data []byte) ([]byte, error) {
	return c.decrypt(ctx, data)
}

// EncryptFile читает файл в память целиком; большие файлы клиент обрабатывает через пакет message.
func (c *CryptoContext) EncryptFile(inPath, outPath string) error {
	return c.processFile(context.Background(), inPath, outPath, c.encrypt)
}

func (c *CryptoContext) DecryptFile(inPath, outPath string) error {
	return c.processFile(context.Background(), inPath, outPath, c.decrypt)
}

func (c *CryptoContext) processFile(ctx context.Context, inPath, outPath string,
	op func(context.Context, []byte) ([]byte, error)) error {
	data, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("cryptoctx: read %s: %w", inPath, err)
	}
	out, err := op(ctx, data)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outPath, out, 0o600); err != nil {
		return fmt.Errorf("cryptoctx: write %s: %w", outPath, err)
	}
	return nil
}

func (c *CryptoContext) EncryptAsync(ctx context.Context, data []byte) <-chan AsyncResult {
	return runAsync(func() ([]byte, error) { return c.encrypt(ctx, data) })
}

func (c *CryptoContext) DecryptAsync(ctx context.Context, data []byte) <-chan AsyncResult {
	return runAsync(func() ([]byte, error) { return c.decrypt(ctx, data) })
}

func (c *CryptoContext) EncryptFileAsync(ctx context.Context, inPath, outPath string) <-chan AsyncResult {
	return runAsync(func() ([]byte, error) {
		return nil, c.processFile(ctx, inPath, outPath, c.encrypt)
	})
}

func (c *CryptoContext) DecryptFileAsync(ctx context.Context, inPath, outPath string) <-chan AsyncResult {
	return runAsync(func() ([]byte, error) {
		return nil, c.processFile(ctx, inPath, outPath, c.decrypt)
	})
}

func runAsync(op func() ([]byte, error)) <-chan AsyncResult {
	ch := make(chan AsyncResult, 1)
	go func() {
		defer close(ch)
		data, err := op()
		ch <- AsyncResult{Data: data, Err: err}
	}()
	return ch
}
