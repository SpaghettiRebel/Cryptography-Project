//go:build !devcipher

package ciphers

import "cryptochat/core/cipher"

// DevBuild — сборка для разработки. В сдаваемой сборке тестового шифра нет.
const DevBuild = false

func substitute(cipher.Algorithm) (cipher.Algorithm, Status) {
	return 0, Status{Note: "алгоритм не реализован в этой сборке"}
}
