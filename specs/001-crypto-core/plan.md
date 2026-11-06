# Implementation Plan: Криптоядро курсового проекта

**Branch**: `001-crypto-core` | **Date**: 2026-11-06 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `/specs/001-crypto-core/spec.md`

## Summary

Go-модуль `core`: общий интерфейс и реестр алгоритмов, режимы и набивки (перенос из `lab3`), Диффи-Хеллман на группе 14
RFC 3526 с собственным `ModPow` из `lab2`, HKDF-SHA256 на `crypto/hmac`, двоичный формат фрагмента и потоковая обработка
сообщений с параллельностью, прогрессом и отменой. Настоящие шифры подключаются в `002-ciphers`, а до этого проверки идут
на тестовом шифре, который не попадает в сдаваемую сборку. Решения и обоснования — в [research.md](research.md).

## Technical Context

**Language/Version**: Go 1.22 (`go 1.22` в `go.mod`; установлен toolchain 1.23.1)

**Primary Dependencies**: только стандартная библиотека: `crypto/rand`, `crypto/hmac`, `crypto/sha256`, `math/big`

**Storage**: нет; сообщения читаются из `io.Reader` и записываются в `io.WriterAt`

**Testing**: `go test`, `go test -race`, нативный fuzzing Go; быстрый набор — `-short`; `go vet`, `gofmt`

**Target Platform**: Windows 11 при разработке; код кроссплатформенный, без cgo; потребители: сервер в Linux-контейнере и клиент на машине пользователя

**Project Type**: библиотека

**Performance Goals**: согласование ключа не дольше 1 с на сторону (измерено около 17 мс); отмена не дольше 1 с; память не больше 16 фрагментов; ускорение не меньше 2 раз на 4 ядрах (SC-007…SC-010)

**Constraints**: без сети, брокера, хранилища и UI; сервер читает открытые поля без кода шифров; без готовых шифров, режимов и обмена ключами; тестовый шифр не в сдаваемой сборке

**Scale/Scope**: 7 режимов, 4 набивки, 3 длины ключа, сообщения от 0 байт до 100 МБ в проверках

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Принцип | Как выполняется | До / после проекта |
|---|---|---|
| I. Собственная реализация | режимы, набивки, DH и `ModPow` свои; `big.Int.Exp` с модулем не используется | ✓ / ✓ |
| II. Готовые примитивы | HKDF на `crypto/hmac` и SHA-256; случайность из `crypto/rand`. `argon2id` относится к серверу | ✓ / ✓ |
| III. Сервер не видит данных | сервер получает только `core/protocol`; метаданные внутри шифротекста | ✓ / ✓ |
| IV. Честная модель угроз | RFC 3526 группа 14; проверка значений; ограничения перечислены в research R9 | ✓ / ✓ |
| V. Эталоны и свойства | Группа сверяется с формулой RFC, HKDF с вектором RFC 5869, режимы с определениями. **Векторы MAGENTA и RC6 и 14 сочетаний — в `002-ciphers`, сквозной тест — в фиче обмена сообщениями** | ✓ / ✓ |
| VI. Клиент не падает | ошибки вместо паники; fuzzing разбора; потоки, прогресс, отмена; независимые фрагменты, метаданные внутри | ✓ / ✓ |
| VII. Качество кода | `gofmt`, `go vet`; единый интерфейс алгоритма | ✓ / ✓ |
| VIII. Архитектура | модуль `core` в `go.work`, без зависимостей от инфраструктуры; тест запрещает `core/protocol` импортировать шифры | ✓ / ✓ |

Часть принципа V намеренно передана другим фичам: это разбиение работы, а не отступление. Нарушений нет.

## Project Structure

### Documentation (this feature)

```text
specs/001-crypto-core/
├── plan.md              # этот файл
├── research.md          # решения и обоснования
├── data-model.md        # сущности и форматы
├── quickstart.md        # как проверить результат
├── contracts/
│   ├── algorithms.md    # интерфейс, реестр, режимы, набивки
│   ├── keyagreement.md  # Диффи-Хеллман и вывод ключа
│   └── fragment.md      # формат фрагмента и API сообщений
└── tasks.md             # создаётся командой /speckit-tasks
```

### Source Code (repository root)

```text
project/
├── go.work                    # use ./core; server и client добавятся в своих фичах
└── core/
    ├── go.mod                 # module cryptochat/core, go 1.22
    ├── cipher/                # BlockCipher, идентификаторы, Settings, ошибки
    ├── algorithms/            # реестр: New(id, keyBits)
    │   ├── registry.go
    │   └── registry_dev.go    # //go:build devcipher
    ├── internal/stubcipher/   # тестовый шифр
    ├── modes/                 # перенос из lab3
    ├── padding/               # перенос из lab3
    ├── cryptoctx/             # перенос cipherctx из lab3 + конструктор для готового шифра
    ├── dh/                    # параметры группы, ModPow, значения, проверка
    ├── kdf/                   # HKDF-SHA256, вывод сеансового ключа
    ├── protocol/              # открытый формат фрагмента, без импорта шифров
    └── message/               # разбиение, шифрование, сборка, прогресс, отмена
```

**Structure Decision**: один модуль с пакетами по обязанностям. Зависимости: `modes ← cipher ← algorithms`; `modes ← padding`; `cipher, padding, modes ← cryptoctx`; `cipher ← kdf`; `protocol, cryptoctx, algorithms ← message`. Пакеты `dh` и `protocol` независимы. Сервер использует только `protocol`.
