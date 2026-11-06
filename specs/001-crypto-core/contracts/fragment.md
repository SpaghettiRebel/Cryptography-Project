# Контракт: фрагмент и обработка сообщений

Структуры полей — в [data-model.md](../data-model.md).

## `core/protocol`

Пакет не импортирует шифры, режимы и `message`; сервер использует только его. Это проверяет тест.

```go
type Fragment struct {
    ChatID, MessageID [16]byte
    Index, Total      uint32
    IV, Cipher        []byte
}

func (f *Fragment) Marshal() ([]byte, error)
func Unmarshal(data []byte) (*Fragment, error) // ErrMalformedFragment, ErrUnsupportedVersion
```

`Unmarshal` не вызывает панику на любых входных данных, проверяет размеры до выделения памяти и отвергает лишние байты.

## `core/message`

```go
type Params struct {
    Settings cipher.Settings
    Key      []byte
    ChatID   [16]byte
}

type Meta struct {
    Name, Type string
    TotalSize  int64
}

type Progress func(doneBytes, totalBytes int64)

func Seal(ctx context.Context, p Params, messageID [16]byte, meta Meta, r io.Reader,
    emit func(*protocol.Fragment) error, progress Progress) error

func NewAssembler(p Params, messageID [16]byte, w io.WriterAt, progress Progress) (*Assembler, error)
func (a *Assembler) Add(ctx context.Context, f *protocol.Fragment) error
func (a *Assembler) Finish() (Meta, error)
```

**`Seal`**: читает `meta.TotalSize` байт, для чтения короче или длиннее возвращает ошибку; отрицательный размер и размер больше 2²⁴ фрагментов — тоже ошибка. Создаёт `Total = max(1, ⌈TotalSize / 1 МиБ⌉)` фрагментов. `emit` вызывается из одной горутины, строго по порядку `Index`; его ошибка останавливает операцию. Каждый фрагмент получает новый IV из `crypto/rand`, если режим его использует.

**`Assembler.Add`**: безопасен при одновременных вызовах; порядок не важен; повтор уже принятого номера игнорируется без сверки содержимого, остаётся первый фрагмент. Каждый вызов держит до трёх объёмов фрагмента, поэтому число одновременных вызовов ограничивает вызывающий код. Несовпадение чата, сообщения, внутренней привязки или размеров даёт `ErrFragmentMismatch`, ошибка расшифрования набивки — `ErrBadPadding`.

**`Finish`**: при пропусках возвращает `ErrMissingFragments`.

**`Progress`**: значения не убывают, последний вызов сообщает полный размер; вызовы не пересекаются. Для пустого сообщения единственный вызов — `(0, 0)`. Колбэк не должен вызывать методы `Assembler`; паника в нём и в `emit` не перехватывается.

**Отмена**: при отмене `ctx` операции возвращают ошибку `context` не позже чем через 1 секунду.
