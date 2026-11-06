package message

import (
	"encoding/binary"
	"fmt"
	"io"

	"cryptochat/core/cipher"
	"cryptochat/core/protocol"
)

const (
	innerVersion = 1
	// Версия, чат, сообщение, номер, полный размер, длины имени, типа и данных.
	innerFixed = 1 + protocol.IDSize + protocol.IDSize + 4 + 8 + 2 + 1 + 4
)

// inner — внутренняя структура, лежащая внутри шифротекста.
type inner struct {
	chatID, messageID [protocol.IDSize]byte
	index             uint32
	totalSize         int64
	name, typ         string
	data              []byte
}

func (in *inner) headerSize() int { return innerFixed + len(in.name) + len(in.typ) }

// putHeader записывает заголовок в начало buf; длина данных уже известна.
func (in *inner) putHeader(buf []byte, dataLen int) {
	buf[0] = innerVersion
	pos := 1
	pos += copy(buf[pos:], in.chatID[:])
	pos += copy(buf[pos:], in.messageID[:])
	binary.BigEndian.PutUint32(buf[pos:], in.index)
	pos += 4
	binary.BigEndian.PutUint64(buf[pos:], uint64(in.totalSize))
	pos += 8
	binary.BigEndian.PutUint16(buf[pos:], uint16(len(in.name)))
	pos += 2
	buf[pos] = byte(len(in.typ))
	pos++
	pos += copy(buf[pos:], in.name)
	pos += copy(buf[pos:], in.typ)
	binary.BigEndian.PutUint32(buf[pos:], uint32(dataLen))
}

// addFill дополняет buf случайными байтами до границы блока, минимум на 1 байт, и делает последний
// байт ненулевым. Это скрывает точную длину в поточных режимах, где набивка не применяется, и не даёт
// набивке Zeros срезать данные: она срезает только нули в конце (исследование решений, R7).
func addFill(buf []byte, r io.Reader) ([]byte, error) {
	n := cipher.BlockSize - len(buf)%cipher.BlockSize
	start := len(buf)
	buf = buf[:start+n]
	if _, err := io.ReadFull(r, buf[start:]); err != nil {
		return nil, fmt.Errorf("message: источник случайности: %w", err)
	}
	if buf[len(buf)-1] == 0 {
		buf[len(buf)-1] = 1
	}
	return buf, nil
}

// build собирает полную внутреннюю структуру. Нужна там, где данные уже в памяти.
func (in *inner) build(r io.Reader) ([]byte, error) {
	buf := make([]byte, in.headerSize()+len(in.data), in.headerSize()+len(in.data)+cipher.BlockSize)
	in.putHeader(buf, len(in.data))
	copy(buf[in.headerSize():], in.data)
	return addFill(buf, r)
}

// parseInner разбирает расшифрованные данные. Любое нарушение даёт ErrFragmentMismatch без паники.
func parseInner(b []byte) (*inner, error) {
	if len(b) < innerFixed+1 || b[0] != innerVersion {
		return nil, ErrFragmentMismatch
	}
	var in inner
	pos := 1
	copy(in.chatID[:], b[pos:])
	pos += protocol.IDSize
	copy(in.messageID[:], b[pos:])
	pos += protocol.IDSize
	in.index = binary.BigEndian.Uint32(b[pos:])
	pos += 4
	size := binary.BigEndian.Uint64(b[pos:])
	pos += 8
	if size > 1<<62 {
		return nil, ErrFragmentMismatch
	}
	in.totalSize = int64(size)
	nameLen := int(binary.BigEndian.Uint16(b[pos:]))
	pos += 2
	typeLen := int(b[pos])
	pos++
	if nameLen > MaxNameLen || typeLen > MaxTypeLen || len(b) < pos+nameLen+typeLen+4 {
		return nil, ErrFragmentMismatch
	}
	in.name = string(b[pos : pos+nameLen])
	pos += nameLen
	in.typ = string(b[pos : pos+typeLen])
	pos += typeLen
	dataLen := binary.BigEndian.Uint32(b[pos:])
	pos += 4
	if dataLen > ChunkSize || uint64(len(b)-pos) < uint64(dataLen)+1 {
		return nil, ErrFragmentMismatch
	}
	in.data = b[pos : pos+int(dataLen)]
	fill := len(b) - pos - int(dataLen)
	if fill < 1 || fill > cipher.BlockSize || b[len(b)-1] == 0 {
		return nil, ErrFragmentMismatch
	}
	return &in, nil
}

// checkBinding сверяет внутреннюю структуру с открытыми полями и правилами размеров.
func (in *inner) checkBinding(f *protocol.Fragment) error {
	if in.chatID != f.ChatID || in.messageID != f.MessageID || in.index != f.Index {
		return ErrFragmentMismatch
	}
	if fragmentCount(in.totalSize) != int64(f.Total) {
		return ErrFragmentMismatch
	}
	if f.Index != 0 && (in.name != "" || in.typ != "") {
		return ErrFragmentMismatch
	}
	want := int64(ChunkSize)
	if f.Index == f.Total-1 {
		want = in.totalSize - int64(f.Total-1)*ChunkSize
	}
	if int64(len(in.data)) != want {
		return ErrFragmentMismatch
	}
	return nil
}
