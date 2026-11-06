// Пакет cipher — общий интерфейс симметричного алгоритма и настройки шифрования.
package cipher

import (
	"errors"
	"fmt"

	"cryptochat/core/modes"
)

// BlockSize — размер блока обоих алгоритмов проекта в байтах.
const BlockSize = 16

var (
	ErrAlgorithmUnavailable = errors.New("cipher: алгоритм недоступен")
	ErrInvalidKeySize       = errors.New("cipher: недопустимая длина ключа")
	ErrInvalidBlockSize     = errors.New("cipher: недопустимая длина блока")
	ErrKeyNotSet            = errors.New("cipher: ключ не задан")
)

// BlockCipher — симметричный алгоритм. После SetKey реализация обязана быть безопасной при
// одновременной обработке блоков, не менять входные срезы и не хранить ссылок на них.
type BlockCipher interface {
	SetKey(key []byte) error
	EncryptBlock(block []byte) ([]byte, error)
	DecryptBlock(block []byte) ([]byte, error)
	BlockSize() int
}

// Algorithm — стабильный идентификатор алгоритма.
type Algorithm uint8

const (
	MAGENTA Algorithm = 1
	RC6     Algorithm = 2
)

func (a Algorithm) String() string {
	switch a {
	case MAGENTA:
		return "MAGENTA"
	case RC6:
		return "RC6"
	}
	return fmt.Sprintf("Algorithm(%d)", uint8(a))
}

// ValidKeyBits сообщает, допустима ли длина ключа в битах.
func ValidKeyBits(keyBits int) bool {
	return keyBits == 128 || keyBits == 192 || keyBits == 256
}

// Settings — настройки шифрования чата.
type Settings struct {
	Algorithm Algorithm
	KeyBits   int
	Mode      modes.CipherMode
	Padding   modes.PaddingMode
}

// Validate проверяет настройки без обращения к реализации алгоритма.
func (s Settings) Validate() error {
	if !ValidKeyBits(s.KeyBits) {
		return ErrInvalidKeySize
	}
	if !s.Mode.IsValid() {
		return fmt.Errorf("cipher: неизвестный режим %s", s.Mode)
	}
	if !s.Padding.IsValid() {
		return fmt.Errorf("cipher: неизвестная набивка %s", s.Padding)
	}
	return nil
}
