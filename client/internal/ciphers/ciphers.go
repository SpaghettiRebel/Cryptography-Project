// Пакет ciphers сообщает, какие алгоритмы есть в этой сборке клиента, и перечисляет параметры чата.
// Доступность берётся из состава core/algorithms: появление реализации ничего не требует менять здесь.
package ciphers

import (
	"errors"
	"slices"

	"cryptochat/core/algorithms"
	"cryptochat/core/cipher"
)

// Status — доступность алгоритма в сборке.
type Status struct {
	Available  bool
	Substitute bool
	Note       string
}

// ErrUnavailable — алгоритма нет в сборке.
var ErrUnavailable = errors.New("ciphers: алгоритм не реализован в этой сборке")

var chatAlgorithms = []cipher.Algorithm{cipher.MAGENTA, cipher.RC6}

// Info возвращает доступность алгоритма чата.
func Info(a cipher.Algorithm) Status {
	if !slices.Contains(chatAlgorithms, a) {
		return Status{Note: "неизвестный алгоритм"}
	}
	if slices.Contains(algorithms.Available(), a) {
		return Status{Available: true}
	}
	_, st := substitute(a)
	return st
}

// Resolve возвращает идентификатор реализации для шифрования: сам алгоритм или тестовый шифр.
// В выводе ключа и на сервере всегда участвует алгоритм чата, а не результат Resolve.
func Resolve(a cipher.Algorithm) (cipher.Algorithm, error) {
	if !slices.Contains(chatAlgorithms, a) {
		return 0, ErrUnavailable
	}
	if slices.Contains(algorithms.Available(), a) {
		return a, nil
	}
	impl, st := substitute(a)
	if !st.Available {
		return 0, ErrUnavailable
	}
	return impl, nil
}

// AlgorithmOption — алгоритм в списке выбора.
type AlgorithmOption struct {
	ID        string
	Algorithm cipher.Algorithm
	Info      Status
}

// Option — режим или набивка в списке выбора.
type Option struct {
	ID      string
	Title   string
	Warning string
}

// ChatOptions — всё, из чего выбирается при создании чата.
type ChatOptions struct {
	Algorithms  []AlgorithmOption
	KeyBits     []int
	CipherModes []Option
	Paddings    []Option
}

// Порядок совпадает с enum CipherMode и PaddingMode контракта: номер = индекс + 1.
var cipherModes = []Option{
	{ID: "ECB", Title: "ECB", Warning: "ECB шифрует одинаковые блоки одинаково и раскрывает повторы в данных"},
	{ID: "CBC", Title: "CBC"},
	{ID: "PCBC", Title: "PCBC"},
	{ID: "CFB", Title: "CFB"},
	{ID: "OFB", Title: "OFB"},
	{ID: "CTR", Title: "CTR"},
	{ID: "RANDOM_DELTA", Title: "Random Delta"},
}

var paddings = []Option{
	{ID: "ZEROS", Title: "Zeros", Warning: "Zeros не отличает нулевые байты в конце данных от набивки"},
	{ID: "ANSI_X923", Title: "ANSI X.923"},
	{ID: "PKCS7", Title: "PKCS7"},
	{ID: "ISO_10126", Title: "ISO 10126"},
}

// Options возвращает варианты для диалога создания чата.
func Options() ChatOptions {
	o := ChatOptions{
		KeyBits:     []int{128, 192, 256},
		CipherModes: slices.Clone(cipherModes),
		Paddings:    slices.Clone(paddings),
	}
	for _, a := range chatAlgorithms {
		o.Algorithms = append(o.Algorithms, AlgorithmOption{ID: a.String(), Algorithm: a, Info: Info(a)})
	}
	return o
}

// AlgorithmByID разбирает идентификатор алгоритма из интерфейса.
func AlgorithmByID(id string) (cipher.Algorithm, bool) {
	for _, a := range chatAlgorithms {
		if a.String() == id {
			return a, true
		}
	}
	return 0, false
}

func number(list []Option, id string) (int, bool) {
	for i, o := range list {
		if o.ID == id {
			return i + 1, true
		}
	}
	return 0, false
}

func idOf(list []Option, n int) string {
	if n < 1 || n > len(list) {
		return ""
	}
	return list[n-1].ID
}

// ModeNumber возвращает номер режима в enum контракта.
func ModeNumber(id string) (int, bool) { return number(cipherModes, id) }

// ModeID возвращает идентификатор режима по номеру enum.
func ModeID(n int) string { return idOf(cipherModes, n) }

// PaddingNumber возвращает номер набивки в enum контракта.
func PaddingNumber(id string) (int, bool) { return number(paddings, id) }

// PaddingID возвращает идентификатор набивки по номеру enum.
func PaddingID(n int) string { return idOf(paddings, n) }
