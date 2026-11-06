package padding

import (
	"crypto/rand"
	"errors"
	"fmt"

	"cryptochat/core/modes"
)

// ErrBadPadding — единая ошибка любого нарушения набивки. Подробностей в тексте нет намеренно:
// значения байтов — это расшифрованные данные, а разные сообщения упрощают атаку на набивку.
var ErrBadPadding = errors.New("padding: invalid padding")

func Pad(data []byte, blockSize int, mode modes.PaddingMode) ([]byte, error) {
	if blockSize <= 0 || blockSize > 255 {
		return nil, fmt.Errorf("padding: block size must be in [1, 255], got %d", blockSize)
	}
	padLen := blockSize - len(data)%blockSize // всегда в [1, blockSize]
	if mode == modes.Zeros && padLen == blockSize {
		padLen = 0
	}
	out := make([]byte, len(data)+padLen)
	copy(out, data)
	tail := out[len(data):]
	switch mode {
	case modes.Zeros:
	case modes.ANSIX923:
		tail[len(tail)-1] = byte(padLen)
	case modes.PKCS7:
		for i := range tail {
			tail[i] = byte(padLen)
		}
	case modes.ISO10126:
		if _, err := rand.Read(tail[:len(tail)-1]); err != nil {
			return nil, fmt.Errorf("padding: random bytes: %w", err)
		}
		tail[len(tail)-1] = byte(padLen)
	default:
		return nil, fmt.Errorf("padding: unknown padding mode %s", mode)
	}
	return out, nil
}

func Unpad(data []byte, blockSize int, mode modes.PaddingMode) ([]byte, error) {
	if blockSize <= 0 || blockSize > 255 {
		return nil, fmt.Errorf("padding: block size must be in [1, 255], got %d", blockSize)
	}
	if len(data)%blockSize != 0 {
		return nil, ErrBadPadding
	}
	switch mode {
	case modes.Zeros:
		end := len(data)
		for end > 0 && data[end-1] == 0 {
			end--
		}
		return append([]byte(nil), data[:end]...), nil
	case modes.ANSIX923, modes.PKCS7, modes.ISO10126:
		if len(data) == 0 {
			return nil, ErrBadPadding
		}
		padLen := int(data[len(data)-1])
		if padLen < 1 || padLen > blockSize || padLen > len(data) {
			return nil, ErrBadPadding
		}
		tail := data[len(data)-padLen : len(data)-1]
		for _, b := range tail {
			if mode == modes.PKCS7 && int(b) != padLen {
				return nil, ErrBadPadding
			}
			if mode == modes.ANSIX923 && b != 0 {
				return nil, ErrBadPadding
			}
		}
		return append([]byte(nil), data[:len(data)-padLen]...), nil
	default:
		return nil, fmt.Errorf("padding: unknown padding mode %s", mode)
	}
}
