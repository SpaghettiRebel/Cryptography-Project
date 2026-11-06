// Пакет stubcipher — тестовый шифр для тестов и разработки клиента.
//
// Это НЕ стойкий шифр и не алгоритм задания: простая обратимая сеть Фейстеля, которая нужна только
// для проверки режимов и формата сообщений, пока нет MAGENTA и RC6. В сдаваемую сборку он не входит.
package stubcipher

import (
	"encoding/binary"
	"math/bits"
	"runtime"
	"sync/atomic"

	"cryptochat/core/cipher"
)

const rounds = 8

// Cipher реализует cipher.BlockCipher с блоком 16 байт.
type Cipher struct {
	keyBytes int
	load     int
	keys     atomic.Pointer[[rounds]uint64]
}

// New создаёт шифр с заданной длиной ключа в битах.
func New(keyBits int) (*Cipher, error) { return NewWithLoad(keyBits, 0) }

// NewWithLoad добавляет искусственную нагрузку на блок, не меняя результат: нужна для замера
// ускорения от параллельности.
func NewWithLoad(keyBits, load int) (*Cipher, error) {
	if !cipher.ValidKeyBits(keyBits) {
		return nil, cipher.ErrInvalidKeySize
	}
	return &Cipher{keyBytes: keyBits / 8, load: load}, nil
}

func (c *Cipher) BlockSize() int { return cipher.BlockSize }

// SetKey выводит раундовые ключи; ссылка на key не сохраняется.
func (c *Cipher) SetKey(key []byte) error {
	if len(key) != c.keyBytes {
		return cipher.ErrInvalidKeySize
	}
	var state uint64 = 0x9E3779B97F4A7C15
	for _, b := range key {
		state = bits.RotateLeft64(state^uint64(b), 11) * 0xBF58476D1CE4E5B9
	}
	var ks [rounds]uint64
	for i := range ks {
		state = bits.RotateLeft64(state+0x9E3779B97F4A7C15, 23) * 0x94D049BB133111EB
		ks[i] = state
	}
	c.keys.Store(&ks)
	return nil
}

func (c *Cipher) f(x, k uint64) uint64 {
	v := bits.RotateLeft64(x+k, 13) ^ (x * 0xFF51AFD7ED558CCD)
	if c.load > 0 {
		sink := v
		for i := 0; i < c.load; i++ {
			sink = bits.RotateLeft64(sink, 7) * 0xC4CEB9FE1A85EC53
		}
		// Результат нагрузки не влияет на v, а KeepAlive не даёт компилятору убрать цикл.
		runtime.KeepAlive(sink)
	}
	return v
}

func (c *Cipher) prepare(block []byte) (*[rounds]uint64, error) {
	ks := c.keys.Load()
	if ks == nil {
		return nil, cipher.ErrKeyNotSet
	}
	if len(block) != cipher.BlockSize {
		return nil, cipher.ErrInvalidBlockSize
	}
	return ks, nil
}

// EncryptBlock шифрует блок и возвращает новый срез.
func (c *Cipher) EncryptBlock(block []byte) ([]byte, error) {
	ks, err := c.prepare(block)
	if err != nil {
		return nil, err
	}
	l := binary.BigEndian.Uint64(block[:8])
	r := binary.BigEndian.Uint64(block[8:])
	for i := 0; i < rounds; i++ {
		l, r = r, l^c.f(r, ks[i])
	}
	out := make([]byte, cipher.BlockSize)
	binary.BigEndian.PutUint64(out[:8], l)
	binary.BigEndian.PutUint64(out[8:], r)
	return out, nil
}

// DecryptBlock расшифровывает блок и возвращает новый срез.
func (c *Cipher) DecryptBlock(block []byte) ([]byte, error) {
	ks, err := c.prepare(block)
	if err != nil {
		return nil, err
	}
	l := binary.BigEndian.Uint64(block[:8])
	r := binary.BigEndian.Uint64(block[8:])
	for i := rounds - 1; i >= 0; i-- {
		l, r = r^c.f(l, ks[i]), l
	}
	out := make([]byte, cipher.BlockSize)
	binary.BigEndian.PutUint64(out[:8], l)
	binary.BigEndian.PutUint64(out[8:], r)
	return out, nil
}
