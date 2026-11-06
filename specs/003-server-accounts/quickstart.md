# Quickstart: проверка результата

**Требования**: Go 1.22 или новее; запущенный Docker Desktop. Команды Go выполняются из папки `project/server`, если не сказано иное.

## 1. Сборка и быстрые тесты

```bash
go build ./...
go vet ./...
gofmt -l .
go test -short ./...
```

Ожидается: без ошибок, `gofmt -l` ничего не печатает. Docker для этого шага не нужен.

## 2. Запуск сервера

Из папки `project`:

```bash
docker compose -f deploy/compose.yaml up --build -d
docker compose -f deploy/compose.yaml ps
```

Ожидается: `server` в состоянии `healthy`, файл `deploy/certs/server.crt` создан.

## 3. Сценарий через тестовый клиент

Из папки `project`, в сессии Git Bash:

```bash
echo "password-alice" | go run ./server/cmd/chatcli -session alice.tmp register alice
echo "password-alice" | go run ./server/cmd/chatcli -session alice.tmp login alice
echo "password-bob01" | go run ./server/cmd/chatcli -session bob.tmp register bob
echo "password-bob01" | go run ./server/cmd/chatcli -session bob.tmp login bob
go run ./server/cmd/chatcli -session alice.tmp contact-request bob
go run ./server/cmd/chatcli -session bob.tmp contact-accept alice
go run ./server/cmd/chatcli -session alice.tmp contacts
```

Ожидается: `bob` в контактах у `alice`.

## 4. Интеграционные и сквозные тесты

```bash
go test -tags e2e ./test/e2e -v -timeout 15m
```

Сквозной тест сам поднимает отдельный проект Compose и проверяет SC-002 и SC-005…SC-009. Ожидается: все тесты зелёные.

SC-001 и SC-004 проверяются на настоящей базе. Запуск временного Postgres:

```bash
docker run --rm -d --name chat-test-db -p 55432:5432 -e POSTGRES_PASSWORD=test postgres:16-alpine
```

Запуск тестов. При `CHAT_REQUIRE_POSTGRES=1` отсутствие базы считается ошибкой, а не пропуском:

```bash
CHAT_TEST_POSTGRES_DSN="postgres://postgres:test@localhost:55432/postgres?sslmode=disable" CHAT_REQUIRE_POSTGRES=1 go test ./internal/postgres -v
```

Пароль `test` относится только к этому временному контейнеру. Остановка:

```bash
docker stop chat-test-db
```

## 5. Проверки по требованиям

| что проверить | команда | ожидается |
|---|---|---|
| Сервисы: FR-001…FR-014 | `go test ./internal/accounts ./internal/contacts -v` | правила выполняются на хранилище в памяти |
| gRPC: авторизация, ошибки, лимиты | `go test ./internal/rpc -v` | ожидаемые коды и причины, журналы без секретов |
| Одновременность: SC-001, SC-004 | `go test ./internal/postgres -v` с `CHAT_TEST_POSTGRES_DSN` | одна регистрация из 100, нет дублей |
| Гонки | `go test -short -race ./...` | гонок нет |

Сервер не должен зависеть от криптоядра, вывод этой команды должен быть пустым:

```bash
go list -deps ./... | grep cryptochat/core
```

## 6. Остановка

```bash
docker compose -f deploy/compose.yaml down -v
```
