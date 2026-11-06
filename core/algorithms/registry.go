// Пакет algorithms создаёт реализации алгоритмов по идентификатору.
package algorithms

import (
	"slices"

	"cryptochat/core/cipher"
)

// factories заполняется только при инициализации пакета. MAGENTA и RC6 добавит фича 002-ciphers.
var factories = map[cipher.Algorithm]func(keyBits int) (cipher.BlockCipher, error){}

// New создаёт шифр без ключа или возвращает cipher.ErrAlgorithmUnavailable.
func New(a cipher.Algorithm, keyBits int) (cipher.BlockCipher, error) {
	factory, ok := factories[a]
	if !ok {
		return nil, cipher.ErrAlgorithmUnavailable
	}
	if !cipher.ValidKeyBits(keyBits) {
		return nil, cipher.ErrInvalidKeySize
	}
	return factory(keyBits)
}

// Available возвращает подключённые алгоритмы по возрастанию идентификатора.
func Available() []cipher.Algorithm {
	out := make([]cipher.Algorithm, 0, len(factories))
	for a := range factories {
		out = append(out, a)
	}
	slices.Sort(out)
	return out
}
