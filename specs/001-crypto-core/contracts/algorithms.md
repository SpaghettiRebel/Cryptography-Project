# Контракт: алгоритмы, режимы, набивки

## `core/cipher`

```go
type BlockCipher interface {
    SetKey(key []byte) error
    EncryptBlock(block []byte) ([]byte, error)
    DecryptBlock(block []byte) ([]byte, error)
    BlockSize() int
}
```

Реализация обязана быть безопасной при одновременной обработке блоков после `SetKey`, не менять входные срезы и не хранить ссылки на них.

Также здесь: `Algorithm`, `Settings`, сентинелы `ErrAlgorithmUnavailable`, `ErrInvalidKeySize`, `ErrInvalidBlockSize`, `ErrKeyNotSet`.

## `core/algorithms`

```go
func New(a cipher.Algorithm, keyBits int) (cipher.BlockCipher, error)
func Available() []cipher.Algorithm
```

До `002-ciphers` запрос `MAGENTA` и `RC6` возвращает `ErrAlgorithmUnavailable`. `002-ciphers` добавляет записи в таблицу реестра и больше ничего не меняет.

## `core/internal/stubcipher`

Только для тестов и, через тег `devcipher`, для разработки клиента. Не является стойким шифром. Без тега не входит в зависимости сборки.

## `core/modes`, `core/padding`

Перенос из `lab3` без изменения поведения: `CipherMode`, `PaddingMode`, `AllCipherModes`, `AllPaddingModes`, `Pad`, `Unpad`, `ErrBadPadding`. При нарушении структуры набивки и длины данных `Unpad` возвращает ровно `ErrBadPadding`; неверный размер блока и неизвестная набивка — отдельные ошибки программиста.

## `core/cryptoctx`

Перенос `cipherctx` из `lab3`:

```go
New(c cipher.BlockCipher, key []byte, mode, padding, opts...) (*CryptoContext, error) // вызывает SetKey; контекст владеет шифром
NewWithCipher(keyed cipher.BlockCipher, mode, padding, opts...) (*CryptoContext, error) // новое: шифр уже с ключом
// опции: WithIV, WithWorkers, WithDelta
Encrypt, Decrypt, EncryptContext, DecryptContext
EncryptFile, DecryptFile, EncryptAsync, DecryptAsync, EncryptFileAsync, DecryptFileAsync
IV, Delta, Mode, Padding, BlockSize, Workers
```

`NewWithCipher` не вызывает `SetKey`, поэтому один шифр с ключом можно использовать во многих контекстах одновременно.

Изменения относительно `lab3`:

- `NewWithCipher`;
- шифротекст недопустимой длины в ECB, CBC, PCBC даёт ровно `padding.ErrBadPadding`;
- результат шифра неверной длины даёт `ErrInvalidBlockSize`, а не портит соседний блок;
- результат `EncryptFile` и `DecryptFile` создаётся с правами `0600`.
