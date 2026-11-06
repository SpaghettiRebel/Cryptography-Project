# Implementation Plan: Основа сервера: пользователи и контакты

**Branch**: `003-server-accounts` | **Date**: 2026-11-06 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `/specs/003-server-accounts/spec.md`

## Summary

Go-модуль `server` в `go.work`: консольное приложение с gRPC поверх TLS 1.3, регистрацией, входом и контактами, хранением в Postgres и запуском одной командой через Docker Compose. Пароли защищены `argon2id`, сессии хранятся как SHA-256. Секреты развёртывания и сертификат создаёт отдельный контейнер при первом запуске. Для проверки есть тестовый клиент `chatcli` и сквозные тесты. Решения и замеры — в [research.md](research.md).

## Technical Context

**Language/Version**: Go 1.22 (`go 1.22` в `go.mod`; установлен toolchain 1.23.1)

**Primary Dependencies**: `google.golang.org/grpc v1.67.3`, `google.golang.org/protobuf v1.34.2`, `github.com/jackc/pgx/v5 v5.7.2`, `golang.org/x/crypto v0.31.0` (только `argon2`). Версии совместимы с Go 1.22, сборка проверена прототипом

**Storage**: Postgres 16; схема создаётся встроенными миграциями при запуске

**Testing**: `go test`, `-race`; служебная логика на хранилище в памяти; интеграционные тесты на настоящем Postgres через `CHAT_TEST_POSTGRES_DSN`; сквозные тесты с тегом `e2e` запускают Compose

**Target Platform**: сервер — Linux-контейнер; разработка и защита — Windows 11 с Docker Desktop; `chatcli` — на машине пользователя

**Project Type**: консольное серверное приложение и развёртывание

**Performance Goals**: вход не дольше 1 с, остальные операции не дольше 200 мс; запуск с нуля не дольше 2 минут после загрузки образов; ответ при недоступном хранилище не дольше 5 с (SC-005, SC-008, SC-009)

**Constraints**: без открытых паролей и сессий в хранилище и журналах; только защищённый канал; без секретов в коде; без шифров чата на сервере

**Scale/Scope**: 2 gRPC-сервиса, 4 таблицы; демонстрационная нагрузка, десятки одновременных запросов

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Принцип | Как выполняется | До / после проекта |
|---|---|---|
| I. Собственная реализация | шифров и DH здесь нет. TLS и создание его сертификата через stdlib относятся к защите канала, принцип это разрешает | ✓ / ✓ |
| II. Готовые примитивы | `argon2id` с уникальной солью, сессии из `crypto/rand`; SHA-256 только для сессий с высокой энтропией, не для паролей | ✓ / ✓ |
| III. Сервер не видит данных | сообщений и ключей в этой фиче нет; модуль не импортирует `core`; пароли и сессии вне журналов | ✓ / ✓ |
| IV. Честная модель угроз | ограничения перечислены в research R10 | ✓ / ✓ |
| V. Эталоны и свойства | тесты на одновременность и настоящий Postgres; криптографии чата здесь нет | ✓ / ✓ |
| VI. Клиент не падает | клиента нет; сервер устойчив к сбоям хранилища и плохим запросам | ✓ / ✓ |
| VII. Качество и записка | `gofmt`, `go vet` в модуле; записка дополняется архитектурой сервера | ✓ / ✓ |
| VIII. Архитектура | модульный монолит, `go.work`, Compose, Postgres, gRPC с TLS, как и требует принцип | ✓ / ✓ |

Нарушений нет.

## Project Structure

### Documentation (this feature)

```text
specs/003-server-accounts/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── accounts.proto    # gRPC API
│   └── deployment.md     # запуск, параметры, тестовый клиент
└── tasks.md              # создаётся командой /speckit-tasks
```

### Source Code (repository root)

```text
project/
├── go.work                     # use ./core ./server
├── proto/cryptochat/v1/accounts.proto
├── buf.yaml, buf.gen.yaml      # генерация через контейнер, код в репозитории
├── server/
│   ├── go.mod                  # module cryptochat/server
│   ├── api/v1/                 # сгенерированный код; единственный пакет, доступный клиенту
│   ├── cmd/
│   │   ├── server/             # консольный сервер и подкоманды init и healthcheck
│   │   └── chatcli/            # тестовый клиент
│   ├── internal/
│   │   ├── accounts/           # регистрация, вход, сессии; правила логина и пароля
│   │   ├── contacts/           # запросы и контакты
│   │   ├── password/           # argon2id: хеш и проверка
│   │   ├── store/              # интерфейс и ошибки; memstore для тестов
│   │   ├── postgres/           # реализация хранилища, миграции
│   │   ├── rpc/                # обработчики gRPC, авторизация, журналирование, ошибки
│   │   └── bootstrap/          # секреты и сертификат при первом запуске
│   └── test/e2e/               # сквозные тесты, тег e2e
├── deploy/
│   ├── compose.yaml
│   ├── server.Dockerfile
│   └── certs/                  # открытый сертификат для клиента, вне репозитория
└── report/runs/
```

**Structure Decision**: сервисы `accounts` и `contacts` не знают о gRPC и Postgres: зависимости идут от `rpc` и `postgres` к ним. Сгенерированный код лежит в `server/api/v1`: `core` по конституции не зависит от сети, а клиенту нужен только этот пакет, не `internal`.
