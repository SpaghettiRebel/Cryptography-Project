# Cryptography-Project

Курсовая работа по курсу криптографии: защищённый чат со сквозным шифрованием. Сообщения шифруются на клиентах собственными реализациями алгоритмов, а сервер пересылает только шифротекст.

## Состояние

| Часть | Статус |
|---|---|
| Криптоядро `core`: режимы, набивки, Диффи–Хеллман, вывод ключа, формат сообщений | готово |
| Сервер `server`: регистрация и вход, контакты, gRPC с TLS 1.3, Postgres, Docker Compose | готово |
| Шифры MAGENTA и RC6 | не реализованы, ждём документацию к алгоритмам |
| Секретные чаты на сервере: создание, закрытие, подключение, обмен открытыми значениями | готово |
| Передача сообщений через RabbitMQ | не начато |
| Клиент на Wails (React, TypeScript, Go) с SQLite | не начато |

## Стек

Go 1.22, gRPC и Protobuf, Postgres, Docker Compose. По плану добавятся RabbitMQ и клиент на Wails. Архитектура: модульный монолит.

## Структура

```text
core/      криптоядро
server/    сервер и тестовый клиент chatcli
proto/     контракт gRPC
integration/ тесты совместной работы криптоядра и сервера
deploy/    Dockerfile и compose.yaml
docs/      задание
report/    пояснительная записка и результаты прогонов
```

## Запуск

Нужны Docker и Go. Команды выполняются из корня репозитория.

```bash
docker compose -f deploy/compose.yaml up --build -d
```

Сервер слушает порт `8443`. Секреты и самоподписанный сертификат создаются при первом запуске, копия сертификата для клиента появляется в `deploy/certs/server.crt`.

Проверка тестовым клиентом (пароль читается из stdin):

```bash
echo "password-alice" | go run ./server/cmd/chatcli -session alice.tmp register alice
```

```bash
echo "password-alice" | go run ./server/cmd/chatcli -session alice.tmp login alice
```

```bash
go run ./server/cmd/chatcli -session alice.tmp contacts
```

Остановка с удалением данных:

```bash
docker compose -f deploy/compose.yaml down -v
```

## Тесты

Быстрые тесты модулей:

```bash
cd core && go test ./...
```

```bash
cd server && go test -short ./...
```

Сквозные тесты поднимают отдельные проекты Compose, Docker должен быть запущен:

```bash
cd server && go test -tags e2e ./test/e2e -v -timeout 15m
```

```bash
cd integration && go test -tags e2e ./... -v -timeout 15m
```
