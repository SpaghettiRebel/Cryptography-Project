// Пакет message разбивает сообщения на независимые зашифрованные фрагменты и собирает их обратно.
package message

import (
	"errors"
	"fmt"
	"runtime"

	"cryptochat/core/algorithms"
	"cryptochat/core/cipher"
	"cryptochat/core/protocol"
)

const (
	// ChunkSize — размер куска данных во всех фрагментах, кроме последнего. Наибольший фрагмент вместе с
	// конвертом должен помещаться в предел запроса транспорта 64 КиБ.
	ChunkSize = 48 << 10
	// MaxNameLen и MaxTypeLen — наибольшие длины имени и типа файла в байтах.
	MaxNameLen = 1024
	MaxTypeLen = 255
	// maxWorkers ограничивает окно одновременной обработки. На один фрагмент в пике приходится
	// внутренняя структура, копия с набивкой и шифротекст: 3 объёма × 5 = 15, в пределах 16 из SC-008.
	maxWorkers = 5
)

var (
	ErrFragmentMismatch = errors.New("message: фрагмент не соответствует сообщению")
	ErrMissingFragments = errors.New("message: получены не все фрагменты")
	ErrSizeMismatch     = errors.New("message: размер данных не соответствует заявленному")
	ErrInvalidMeta      = errors.New("message: недопустимые метаданные")
)

// Params — настройки чата и сеансовый ключ.
type Params struct {
	Settings cipher.Settings
	Key      []byte
	ChatID   [protocol.IDSize]byte
}

// Meta — метаданные сообщения; передаются только внутри шифротекста.
type Meta struct {
	Name, Type string
	TotalSize  int64
}

// Progress сообщает обработанный объём. Значения не убывают, вызовы не пересекаются. Для пустого
// сообщения единственный вызов — (0, 0): вызывающий код считает его завершением, а не делит на ноль.
// Колбэк не должен вызывать методы Assembler: он выполняется под его блокировкой. Паника в колбэке
// или emit не перехватывается, это ошибка вызывающего кода.
type Progress func(doneBytes, totalBytes int64)

func (p Params) validate() error {
	if err := p.Settings.Validate(); err != nil {
		return err
	}
	if len(p.Key)*8 != p.Settings.KeyBits {
		return cipher.ErrInvalidKeySize
	}
	return nil
}

// keyedCipher создаёт шифр выбранного алгоритма с заданным ключом.
func (p Params) keyedCipher() (cipher.BlockCipher, error) {
	c, err := algorithms.New(p.Settings.Algorithm, p.Settings.KeyBits)
	if err != nil {
		return nil, err
	}
	if err := c.SetKey(p.Key); err != nil {
		return nil, fmt.Errorf("message: ключ: %w", err)
	}
	return c, nil
}

// fragmentCount — max(1, ⌈size / ChunkSize⌉).
func fragmentCount(size int64) int64 {
	if size <= 0 {
		return 1
	}
	return (size-1)/ChunkSize + 1
}

func defaultWorkers() int { return min(runtime.GOMAXPROCS(0), maxWorkers) }
