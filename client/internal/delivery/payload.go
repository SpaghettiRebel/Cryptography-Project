package delivery

import (
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"cryptochat/core/message"
)

// Соглашения о содержимом внутри шифротекста. Сервер их не видит.
const (
	TextType   = "text/plain; charset=utf-8"
	CancelType = "application/vnd.cryptochat.cancel"
	binaryType = "application/octet-stream"

	MaxText  = 64 << 10  // байт UTF-8
	MaxFile  = 256 << 20 // байт
	MaxImage = 20 << 20  // наибольшая картинка, показываемая в ленте
	// MaxTotal — наибольшее число частей входящего сообщения: ⌈256 МиБ / 48 КиБ⌉.
	MaxTotal = (MaxFile + message.ChunkSize - 1) / message.ChunkSize

	defaultName = "файл"
	textName    = "сообщение.txt"
)

// Kind — вид содержимого после расшифрования.
type Kind int

const (
	KindText Kind = iota + 1
	KindFile
	KindImage
	KindCancel
)

// Classified — результат классификации расшифрованного сообщения.
type Classified struct {
	Kind      Kind
	Name      string // очищенное имя файла
	MediaType string // тип картинки по содержимому
}

// TextMeta — метаданные текстового сообщения.
func TextMeta(text string) message.Meta {
	return message.Meta{Type: TextType, TotalSize: int64(len(text))}
}

// FileMeta — метаданные файла: имя без пути, не длиннее MaxNameLen байт по границе символа, тип по
// расширению или application/octet-stream.
func FileMeta(path string, size int64) message.Meta {
	name := truncateUTF8(filepath.Base(path), message.MaxNameLen)
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = defaultName
	}
	typ := mime.TypeByExtension(filepath.Ext(path))
	if typ == "" || len(typ) > message.MaxTypeLen {
		typ = binaryType
	}
	return message.Meta{Name: name, Type: typ, TotalSize: size}
}

// CancelPayload — служебное сообщение отмены сообщения id и его данные.
func CancelPayload(id [16]byte) (message.Meta, []byte) {
	return message.Meta{Type: CancelType, TotalSize: cancelSize}, append([]byte(nil), id[:]...)
}

// cancelSize — размер служебного сообщения отмены: идентификатор отменяемого сообщения.
const cancelSize = 16

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// normalizeType приводит тип к нижнему регистру и убирает пробелы вокруг ';'.
func normalizeType(t string) string {
	parts := strings.Split(strings.ToLower(t), ";")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ";")
}

var imageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "image/bmp": true}

// Classify определяет вид сообщения по метаданным и первым байтам содержимого. Картинка определяется
// только по содержимому: заявленный тип не решает.
func Classify(meta message.Meta, head []byte, size int64) Classified {
	typ := normalizeType(meta.Type)
	if meta.Name == "" {
		switch typ {
		case normalizeType(TextType):
			if size > MaxText {
				return Classified{Kind: KindFile, Name: textName}
			}
			return Classified{Kind: KindText}
		case normalizeType(CancelType):
			// Служебная отмена — ровно 16 байт идентификатора; другой размер — обычный файл.
			if size == cancelSize {
				return Classified{Kind: KindCancel}
			}
		}
	}
	c := Classified{Kind: KindFile, Name: SafeFileName(meta.Name)}
	if size <= MaxImage && len(head) > 0 {
		if detected := http.DetectContentType(head); imageTypes[detected] {
			c.Kind, c.MediaType = KindImage, detected
		}
	}
	return c
}

var reserved = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true}

// SafeFileName делает из имени отправителя имя, пригодное только как предложение в диалоге сохранения:
// без пути, управляющих и невидимых символов форматирования (в том числе смены направления письма)
// и знаков <>:"|?*, не зарезервированное в Windows, без точек и пробелов
// в конце, не длиннее 255 символов; пустое — «файл».
func SafeFileName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == utf8.RuneError, r < 0x20, r == 0x7f, strings.ContainsRune(`<>:"|?*`, r),
			unicode.Is(unicode.Cf, r), unicode.Is(unicode.Cc, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	name = b.String()
	if r := []rune(name); len(r) > 255 {
		name = string(r[:255])
	}
	name = strings.TrimRight(name, ". ")
	base := strings.ToUpper(strings.TrimSpace(strings.SplitN(name, ".", 2)[0]))
	if reserved[base] {
		name = "_" + name
	}
	if name == "" {
		return defaultName
	}
	return name
}
