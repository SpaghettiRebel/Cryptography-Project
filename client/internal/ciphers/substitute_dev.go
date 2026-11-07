//go:build devcipher

package ciphers

import (
	"cryptochat/core/algorithms"
	"cryptochat/core/cipher"
)

// DevBuild — сборка для разработки: вместо отсутствующих алгоритмов подставляется тестовый шифр.
const DevBuild = true

func substitute(a cipher.Algorithm) (cipher.Algorithm, Status) {
	return algorithms.DevAlgorithm, Status{
		Available:  true,
		Substitute: true,
		Note:       "тестовый шифр вместо " + a.String() + ", только для разработки",
	}
}
