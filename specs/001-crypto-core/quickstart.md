# Quickstart: проверка результата

**Требования**: Go 1.22 или новее. Команды выполняются из папки `project/core`.

## 1. Сборка и статика

```bash
go build ./...
go vet ./...
gofmt -l .
```

Ожидается: без ошибок, `gofmt -l` ничего не печатает.

## 2. Тесты

```bash
go test -short ./...
go test ./...
go test -short -race ./...
```

Ожидается: все тесты зелёные, гонок нет. Большие файлы и лимиты времени проверяются без `-race`.

## 3. Проверки по требованиям

| что проверить | команда | ожидается |
|---|---|---|
| Режимы и набивки: SC-001, SC-002 | `go test ./cryptoctx -v -run "Matrix$"`, затем `-run "ModesByDefinition$"` | все сочетания восстановлены, режимы совпали с определениями |
| Диффи-Хеллман и ключ: SC-003, SC-010 | `go test ./dh ./kdf -v` | 1000 согласований, группа и RFC 5869 совпали |
| Сообщения: SC-004, SC-011 | `go test ./message -v -run "RoundTrip"`, затем `-run "OpenFieldsLeak$"` | 100 МБ восстановлены, утечек нет |
| Разбор: SC-005 | `go test ./protocol -run "CorruptedInputs$"` | 100 000 вариантов без паники |
| Fuzzing разбора | `go test ./protocol -fuzz FuzzUnmarshal -fuzztime 30s` | нет падений |
| Параллельность: SC-006, SC-007 | `go test ./message -v -run "Parallel"` | результат совпал; ускорение не меньше 2 раз на 4 ядрах |
| Память, отмена, прогресс: SC-008, SC-009 | `go test ./message -v -run "Memory$"`, затем `-run "Cancel$"` и `-run "Progress$"` | рост не больше 16 фрагментов; отмена не дольше 1 с; прогресс монотонен |
| Тестовый шифр: SC-012 | `go test ./algorithms -v` | в сборке без тега его нет; MAGENTA и RC6 дают `ErrAlgorithmUnavailable` |

## 4. Запреты и границы

```bash
go list -deps ./protocol
go list -f "{{.ImportPath}}: {{join .Imports \" \"}}" ./...
```

Ожидается: в зависимостях `core/protocol` нет `cipher`, `algorithms`, `cryptoctx` и `message`; прямых импортов `crypto/aes`, `crypto/cipher` и `golang.org/x/crypto` нет.

Запуск с заглушкой для разработки:

```bash
go test -tags devcipher ./algorithms
```

Ожидается: идентификатор `255` доступен только с этим тегом.
