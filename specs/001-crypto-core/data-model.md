# Data Model: Криптоядро курсового проекта

## Алгоритм и настройки

**Algorithm** — `uint8`: `MAGENTA = 1`, `RC6 = 2`; `255` — тестовый шифр, существует только с тегом `devcipher`.

**Settings**:

| поле | значения | правило |
|---|---|---|
| `Algorithm` | см. выше | нет реализации → `ErrAlgorithmUnavailable` |
| `KeyBits` | 128, 192, 256 | иначе `ErrInvalidKeySize` |
| `Mode` | ECB, CBC, PCBC, CFB, OFB, CTR, RandomDelta | иначе ошибка |
| `Padding` | Zeros, ANSIX923, PKCS7, ISO10126 | иначе ошибка; применяется только в ECB, CBC, PCBC |

Блок всегда 16 байт.

## Значения Диффи-Хеллмана

- **Group**: `P` (2048 бит), `G = 2`; значение неизменяемо.
- **KeyPair**: секретное значение в [2, p − 2], новое на каждое согласование; открытое `G^x mod P`. Секретное значение не выводится в строковое представление.
- **Правило открытого значения**: допустимо только 2 ≤ y ≤ p − 2.
- **Общий секрет**: ровно 256 байт, big-endian, с ведущими нулями.

## Сеансовый ключ

HKDF-SHA256 от общего секрета; `salt = ChatID`; `info = "cryptochat/session-key/v1" ‖ Algorithm ‖ KeyBits (2 байта)`. Длина = `KeyBits / 8`.

## Фрагмент (открытая часть)

| поле | размер | правило |
|---|---|---|
| Magic | 2 байта | `"CF"` |
| Version | 1 байт | `1` |
| ChatID | 16 байт | |
| MessageID | 16 байт | |
| Index | 4 байта | `Index < Total` |
| Total | 4 байта | `1 ≤ Total ≤ 2²⁴` |
| IVLen | 1 байт | 0 для ECB, иначе 16 |
| IV | IVLen | |
| CipherLen | 4 байта | `1 ≤ CipherLen ≤ 1 МиБ + 4 КиБ`, проверяется до выделения памяти |
| Cipher | CipherLen | |

После `Cipher` лишних байтов быть не должно. `Magic`, `IVLen` и `CipherLen` — служебные поля разметки.

## Внутренняя структура (внутри шифротекста)

| поле | размер | правило |
|---|---|---|
| Version | 1 байт | `1` |
| ChatID, MessageID | по 16 байт | равны открытым полям |
| Index | 4 байта | равен открытому полю |
| TotalSize | 8 байт | полный размер данных; одинаков во всех фрагментах |
| NameLen, TypeLen | 2 и 1 байт | больше нуля только во фрагменте 0; имя до 1024 байт, тип до 255 |
| Name, Type | NameLen, TypeLen | |
| DataLen | 4 байта | до 1 МиБ; все фрагменты, кроме последнего, ровно 1 МиБ |
| Data | DataLen | |
| Fill | ≥ 1 байта | случайные байты до границы блока, последний ненулевой |

## Meta

`Name`, `Type`, `TotalSize`. Пустое сообщение: `TotalSize = 0`, `Total = 1`, `DataLen = 0`.

## Assembler и его состояния

```text
Открыт ──Add(фрагмент)──► Открыт
   │  Add: повтор принятого номера игнорируется, остаётся первый фрагмент; несовпадение привязки, чата или сообщения → ошибка
   ├──Finish, все номера получены ──► Завершён (Meta)
   └──Finish, есть пропуски       ──► ErrMissingFragments (сборка остаётся открытой)
```

- **Поля**: настройки, ключ, ChatID, MessageID, `Total` из первого фрагмента, битовая карта номеров, `Meta` из фрагмента 0, назначение `io.WriterAt`.
- **Правило согласованности**: `Total` и `TotalSize` одинаковы во всех фрагментах, `Total = max(1, ⌈TotalSize / 1 МиБ⌉)`, а сумма `DataLen` равна `TotalSize`.

## Ошибки

Сентинелы проверяются через `errors.Is`: `ErrAlgorithmUnavailable`, `ErrInvalidKeySize`, `ErrInvalidBlockSize`, `ErrKeyNotSet`, `ErrBadPadding`, `ErrInvalidPublicValue`, `ErrMalformedFragment`, `ErrUnsupportedVersion`, `ErrFragmentMismatch`, `ErrMissingFragments`. Отмена возвращает ошибку `context`. Ни одна ошибка не содержит ключей, секретов и данных.
