// Пакет password хеширует пароли готовой функцией argon2id.
package password

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

	"golang.org/x/crypto/argon2"
)

// Второй рекомендованный набор RFC 9106, раздел 4.
const (
	timeCost  = 3
	memoryKiB = 64 * 1024
	threads   = 4
	saltLen   = 16
	keyLen    = 32
	// Пределы разбора: подменённая строка в базе не должна потребовать произвольно много ресурсов.
	maxMemory  = 256 * 1024 // 256 МиБ
	maxTime    = 16
	maxThreads = 16
	// maxConcurrent ограничивает память хеширования: 4 × 64 МиБ = 256 МиБ.
	maxConcurrent = 4
)

var (
	ErrMismatch  = errors.New("password: пароль не совпадает")
	ErrMalformed = errors.New("password: некорректная строка хеша")
)

var b64 = base64.RawStdEncoding

type params struct {
	time, memory uint32
	threads      uint8
}

// Hasher выполняет хеширование с ограничением числа одновременных вычислений.
type Hasher struct {
	slots  chan struct{}
	active atomic.Int32
	peak   atomic.Int32
}

// New создаёт Hasher.
func New() *Hasher { return &Hasher{slots: make(chan struct{}, maxConcurrent)} }

func (h *Hasher) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case h.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	n := h.active.Add(1)
	for {
		p := h.peak.Load()
		if n <= p || h.peak.CompareAndSwap(p, n) {
			return nil
		}
	}
}

func (h *Hasher) release() {
	h.active.Add(-1)
	<-h.slots
}

func derive(p params, salt []byte, pw string) []byte {
	return argon2.IDKey([]byte(pw), salt, p.time, p.memory, p.threads, keyLen)
}

func encode(p params, salt []byte, pw string) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.memory, p.time, p.threads,
		b64.EncodeToString(salt), b64.EncodeToString(derive(p, salt, pw)))
}

// Hash возвращает PHC-строку с новой случайной солью.
func (h *Hasher) Hash(ctx context.Context, pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: источник случайности: %w", err)
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	return encode(params{time: timeCost, memory: memoryKiB, threads: threads}, salt, pw), nil
}

func parse(encoded string) (params, []byte, []byte, error) {
	var p params
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return p, nil, nil, ErrMalformed
	}
	fields := strings.Split(parts[3], ",")
	if len(fields) != 3 {
		return p, nil, nil, ErrMalformed
	}
	read := func(s, prefix string, limit uint64) (uint64, error) {
		value, ok := strings.CutPrefix(s, prefix)
		if !ok {
			return 0, ErrMalformed
		}
		v, err := strconv.ParseUint(value, 10, 32)
		if err != nil || v == 0 || v > limit {
			return 0, ErrMalformed
		}
		return v, nil
	}
	m, err := read(fields[0], "m=", maxMemory)
	if err != nil {
		return p, nil, nil, err
	}
	t, err := read(fields[1], "t=", maxTime)
	if err != nil {
		return p, nil, nil, err
	}
	par, err := read(fields[2], "p=", maxThreads)
	if err != nil {
		return p, nil, nil, err
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return p, nil, nil, ErrMalformed
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) != keyLen {
		return p, nil, nil, ErrMalformed
	}
	return params{time: uint32(t), memory: uint32(m), threads: uint8(par)}, salt, key, nil
}

// Verify проверяет пароль по PHC-строке, сравнивая результат за постоянное время.
func (h *Hasher) Verify(ctx context.Context, pw, encoded string) error {
	p, salt, want, err := parse(encoded)
	if err != nil {
		return err
	}
	if err := h.acquire(ctx); err != nil {
		return err
	}
	defer h.release()
	got := derive(p, salt, pw)
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}
