# Cryptography-Project

Курсовая работа по курсу криптографии: защищённый чат со сквозным шифрованием. Сообщения шифруются на клиентах собственными реализациями алгоритмов, а сервер пересылает только шифротекст.

## Состояние

| Часть | Статус |
|---|---|
| Криптоядро `core`: режимы, набивки, Диффи–Хеллман, вывод ключа, формат сообщений | готово |
| Сервер `server`: регистрация и вход, контакты, gRPC с TLS 1.3, Postgres, Docker Compose | готово |
| Шифры MAGENTA и RC6 | не реализованы, ждём документацию к алгоритмам |
| Секретные чаты на сервере: создание, закрытие, подключение, обмен открытыми значениями | готово |
| Передача шифротекста через RabbitMQ: отправка, поток получения, подтверждения, удаление очередей | готово |
| Клиент на Wails (React, TypeScript, Go) с SQLite: вход, контакты, секретные чаты, согласование ключа | готова основа |
| Клиент: ввод, шифрование и передача сообщений и файлов | в работе |

## Стек

Go 1.22, gRPC и Protobuf, Postgres, RabbitMQ, Docker Compose. Клиент: Wails v2.9.2, React 18 и TypeScript, локальная база SQLite без cgo (`modernc.org/sqlite`). Архитектура: модульный монолит.

## Структура

```text
core/      криптоядро
server/    сервер и тестовый клиент chatcli
client/    оконный клиент на Wails: Go в internal/, интерфейс в frontend/
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

Сервер слушает порт `8443`. Секреты и самоподписанный сертификат создаются при первом запуске, копия сертификата для клиента появляется в `deploy/certs/server.crt`. Брокер RabbitMQ запускается вместе с сервером и наружу портов не открывает; пароль брокера применяется при первом запуске с пустым томом, поэтому тома удаляются вместе, командой `down -v`.

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

Передача фрагментов между участниками чата, готового к обмену. Тестовый клиент не шифрует данные: он передаёт готовые байты в шестнадцатеричном виде, а настоящий шифротекст создаёт криптоядро клиента:

```bash
openssl rand -hex 1024 | go run ./server/cmd/chatcli -session alice.tmp send "$ID"
```

```bash
go run ./server/cmd/chatcli -session bob.tmp recv "$ID" 1
```

Остановка с удалением данных:

```bash
docker compose -f deploy/compose.yaml down -v
```

## Клиент

Нужны Go, Docker и Wails CLI v2.9.2. Node не нужен: интерфейс собирается и тестируется в контейнере `node:22-alpine`, зависимости лежат в томе Docker `cryptochat-client-node-modules`.

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.9.2
```

Сборка приложения (`client/build/bin/cryptochat.exe`); вариант с тегом `devcipher` подставляет тестовый шифр вместо отсутствующих MAGENTA и RC6 и только для разработки:

```bash
sh client/scripts/build.sh
```

```bash
sh client/scripts/build.sh -tags devcipher
```

При первом запуске в настройках сервера указываются адрес `localhost:8443` и файл сертификата `deploy/certs/server.crt` (если приложение запущено из корня репозитория, путь подставляется сам). Данные клиента лежат в `%AppData%\cryptochat`; папку можно задать переменной `CRYPTOCHAT_HOME`, например чтобы запустить двух пользователей на одной машине:

```bash
CRYPTOCHAT_HOME="$(pwd -W)/out/alice" client/build/bin/cryptochat.exe &
CRYPTOCHAT_HOME="$(pwd -W)/out/bob" client/build/bin/cryptochat.exe &
```

Ограничение: сеансовые ключи и секретные значения Диффи–Хеллмана хранятся в локальной базе профиля без отдельной защиты, их защищают только права доступа к папке пользователя.

## Тесты

Быстрые тесты модулей:

```bash
cd core && go test ./...
```

```bash
cd core && go test -tags devcipher ./...
```

```bash
cd server && go test -short ./...
```

Тесты на настоящих брокере и базе нужны контейнеры (пример адресов):

```bash
docker run --rm -d --name chat-test-rmq -p 127.0.0.1:35672:5672 -e RABBITMQ_DEFAULT_USER=test -e RABBITMQ_DEFAULT_PASS=test rabbitmq:4.1-alpine
docker run --rm -d --name chat-test-db -p 55432:5432 -e POSTGRES_PASSWORD=test postgres:16-alpine
```

```bash
cd server && CHAT_TEST_RABBITMQ_URL="amqp://test:test@127.0.0.1:35672/" CHAT_REQUIRE_RABBITMQ=1 go test ./internal/rabbit -v
```

```bash
cd server && CHAT_TEST_POSTGRES_DSN="postgres://postgres:test@localhost:55432/postgres?sslmode=disable" CHAT_REQUIRE_POSTGRES=1 go test ./internal/postgres -v
```

Сквозные тесты поднимают отдельные проекты Compose, Docker должен быть запущен:

```bash
cd server && go test -tags e2e ./test/e2e -v -timeout 20m
```

Два клиента с криптоядром передают текст и файл через настоящий брокер. Пока шифры MAGENTA и RC6 не реализованы, тест использует тестовый шифр из тега `devcipher`; в сдаваемую сборку он не входит:

```bash
cd integration && go test -tags e2e,devcipher ./... -v -timeout 20m
```

С одним тегом `e2e` запускается только проверка согласования ключа:

```bash
cd integration && go test -tags e2e ./... -v -timeout 20m
```

Клиент: быстрые тесты в сдаваемой сборке и со сборкой `devcipher`:

```bash
cd client && go test -short ./...
```

```bash
cd client && go test -short -tags devcipher ./...
```

Сквозные тесты клиента поднимают проект Compose `cryptochat-client-e2e` на порту 18445:

```bash
cd client && go test -tags e2e ./internal/e2e -v -timeout 20m
```

```bash
cd client && go test -tags e2e,devcipher ./internal/e2e -v -timeout 20m
```

Сборка и тесты интерфейса в контейнере:

```bash
sh client/scripts/frontend.sh
```
