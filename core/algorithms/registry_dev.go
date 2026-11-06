//go:build devcipher

package algorithms

import (
	"cryptochat/core/cipher"
	"cryptochat/core/internal/stubcipher"
)

// DevAlgorithm — идентификатор тестового шифра; существует только с тегом devcipher.
const DevAlgorithm cipher.Algorithm = 255

func init() {
	factories[DevAlgorithm] = func(keyBits int) (cipher.BlockCipher, error) {
		return stubcipher.New(keyBits)
	}
}
