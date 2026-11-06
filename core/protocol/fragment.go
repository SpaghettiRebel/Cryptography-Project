// Пакет protocol описывает открытый двоичный формат фрагмента. Для сервера фрагмент непрозрачен: он не
// импортирует криптоядро. Пакет не импортирует шифры, режимы и обработку сообщений.
package protocol

import (
	"encoding/binary"
	"errors"
)

const (
	// Version — текущая версия формата.
	Version = 1
	// IDSize — длина идентификаторов чата и сообщения в байтах.
	IDSize = 16
	// IVSize — длина IV, если он есть.
	IVSize = 16
	// MaxTotal — наибольшее число фрагментов в сообщении.
	MaxTotal = 1 << 24
	// MaxCipherLen — наибольшая длина шифротекста фрагмента: 48 КиБ данных и 4 КиБ служебных байтов.
	MaxCipherLen = 52 << 10
	// MaxWireSize — наибольший размер сериализованного фрагмента.
	MaxWireSize = fixedSize + IVSize + MaxCipherLen

	// Метка, версия, идентификаторы, номер, общее число, длина IV, длина шифротекста.
	fixedSize = 2 + 1 + IDSize + IDSize + 4 + 4 + 1 + 4
)

var (
	ErrMalformedFragment  = errors.New("protocol: некорректный фрагмент")
	ErrUnsupportedVersion = errors.New("protocol: неподдерживаемая версия фрагмента")
)

var magic = [2]byte{'C', 'F'}

// Fragment — открытые поля фрагмента.
type Fragment struct {
	ChatID, MessageID [IDSize]byte
	Index, Total      uint32
	IV, Cipher        []byte
}

func (f *Fragment) validate() error {
	if f.Total < 1 || f.Total > MaxTotal || f.Index >= f.Total {
		return ErrMalformedFragment
	}
	if len(f.IV) != 0 && len(f.IV) != IVSize {
		return ErrMalformedFragment
	}
	if len(f.Cipher) < 1 || len(f.Cipher) > MaxCipherLen {
		return ErrMalformedFragment
	}
	return nil
}

// Marshal сериализует фрагмент в big-endian.
func (f *Fragment) Marshal() ([]byte, error) {
	if err := f.validate(); err != nil {
		return nil, err
	}
	out := make([]byte, 0, fixedSize+len(f.IV)+len(f.Cipher))
	out = append(out, magic[0], magic[1], Version)
	out = append(out, f.ChatID[:]...)
	out = append(out, f.MessageID[:]...)
	out = binary.BigEndian.AppendUint32(out, f.Index)
	out = binary.BigEndian.AppendUint32(out, f.Total)
	out = append(out, byte(len(f.IV)))
	out = append(out, f.IV...)
	out = binary.BigEndian.AppendUint32(out, uint32(len(f.Cipher)))
	out = append(out, f.Cipher...)
	return out, nil
}

// Unmarshal разбирает фрагмент. Паники нет на любых данных; размеры проверяются до выделения памяти,
// а результат не ссылается на data.
func Unmarshal(data []byte) (*Fragment, error) {
	if len(data) < fixedSize || data[0] != magic[0] || data[1] != magic[1] {
		return nil, ErrMalformedFragment
	}
	if data[2] != Version {
		return nil, ErrUnsupportedVersion
	}
	var f Fragment
	pos := 3
	copy(f.ChatID[:], data[pos:])
	pos += IDSize
	copy(f.MessageID[:], data[pos:])
	pos += IDSize
	f.Index = binary.BigEndian.Uint32(data[pos:])
	f.Total = binary.BigEndian.Uint32(data[pos+4:])
	pos += 8
	ivLen := int(data[pos])
	pos++
	if ivLen != 0 && ivLen != IVSize {
		return nil, ErrMalformedFragment
	}
	if len(data) < pos+ivLen+4 {
		return nil, ErrMalformedFragment
	}
	ivStart := pos
	pos += ivLen
	cipherLen := binary.BigEndian.Uint32(data[pos:])
	pos += 4
	if cipherLen < 1 || cipherLen > MaxCipherLen || uint64(len(data)-pos) != uint64(cipherLen) {
		return nil, ErrMalformedFragment
	}
	if f.Total < 1 || f.Total > MaxTotal || f.Index >= f.Total {
		return nil, ErrMalformedFragment
	}
	if ivLen > 0 {
		f.IV = append([]byte(nil), data[ivStart:ivStart+ivLen]...)
	}
	f.Cipher = append([]byte(nil), data[pos:]...)
	return &f, nil
}
