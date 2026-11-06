# Tasks: Основа сервера: пользователи и контакты

**Input**: Design documents from `/specs/003-server-accounts/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/

**Tests**: обязательны по конституции и критериям SC-001…SC-010. Тесты каждой истории пишутся перед реализацией и сначала падают.

**Organization**: задачи сгруппированы по историям из spec.md; все три истории P1. US2 зависит от авторизации из US1, а US3 использует обе.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: можно выполнять параллельно: разные файлы и нет зависимости от незавершённых задач
- **[Story]**: история из spec.md
- Пути указаны относительно папки `project`; команды Go выполняются из `project/server`

## Общие правила

- Версии зависимостей зафиксированы: `google.golang.org/grpc v1.67.3`, `google.golang.org/protobuf v1.34.2`, `github.com/jackc/pgx/v5 v5.7.2`, `golang.org/x/crypto v0.31.0`. `go.mod` остаётся на `go 1.22`.
- Сервер не импортирует `cryptochat/core`. Python запрещён. Разрешены `crypto/rand`, `crypto/sha256`, `golang.org/x/crypto/argon2`, а `crypto/tls`, `crypto/x509`, `crypto/ecdsa`, `crypto/elliptic` — только для TLS канала и его сертификата.
- Пароли, сессии, хеши паролей и секреты развёртывания не попадают в журналы и тексты ошибок.
- Ошибки сервисов — сентинелы, проверяются через `errors.Is`; наружу они выходят как код gRPC и `ErrorInfo` с причиной из research R7, домен `cryptochat`.
- Комментарии в коде на русском.
- Тесты длиннее ~1 с или требующие Docker пропускаются под `testing.Short()`. Перед отметкой задачи `go build ./...`, `go vet ./...` и тесты затронутого пакета должны быть зелёными.
- Для работы нужен запущенный Docker Desktop: генерация кода и тесты с базой. Если Docker недоступен, задача не отмечается выполненной и об этом сообщается.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: модуль, API и каркас развёртывания

- [X] T001 Создать `server/go.mod` (`module cryptochat/server`, `go 1.22`, зафиксированные версии зависимостей), добавить `./server` в `go.work`, добавить в `.gitignore` строки `deploy/certs/` и `*.tmp`, создать каталоги по дереву из `specs/003-server-accounts/plan.md`. Запустить временный Postgres для тестов командой из раздела 4 `specs/003-server-accounts/quickstart.md` и задать `CHAT_TEST_POSTGRES_DSN`
- [X] T002 Скопировать `specs/003-server-accounts/contracts/accounts.proto` в `proto/cryptochat/v1/accounts.proto`, создать `buf.yaml` и `buf.gen.yaml` с удалёнными плагинами `buf.build/protocolbuffers/go:v1.34.2` и `buf.build/grpc/go:v1.5.1`, запустить генерацию из контейнера `bufbuild/buf` с закреплённым тегом версии (не `latest`) и записать код в `server/api/v1/`; команду генерации указать в комментарии `buf.gen.yaml`. Если удалённые плагины недоступны, собрать в контейнере `golang:1.23-alpine` с `protoc` и плагинами `protoc-gen-go@v1.34.2` и `protoc-gen-go-grpc@v1.5.1` и записать фактический способ в `specs/003-server-accounts/research.md`, раздел R2

**Checkpoint**: `go build ./...` в `server` выполняется без ошибок, код `api/v1` компилируется с `go 1.22`.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: хранилище, пароли, TLS и каркас gRPC нужны всем историям.

**⚠️ CRITICAL**: истории не начинаются, пока фаза не завершена.

- [X] T003 Реализовать в `server/internal/store/store.go` типы `User`, `UserRef`, `ContactLists`, интерфейсы `AccountStore` и `ContactStore` и сентинелы `ErrLoginTaken`, `ErrNotFound`, `ErrSelfRequest`, `ErrAlreadyContact`, `ErrDuplicateRequest`, `ErrRequestNotFound`, `ErrContactNotFound`, `ErrUnavailable`
- [X] T004 [P] Написать `server/internal/password/password_test.go`: хеш и проверка пароля; два хеша одного пароля различаются; неверный пароль и повреждённая строка дают ошибку без паники; параметры и соль читаются из PHC-строки; ограничение одновременных вычислений
- [X] T005 Реализовать в `server/internal/password/password.go` `argon2id` с параметрами `t = 3`, `m = 64 МиБ`, `p = 4`, соль 16 байт из `crypto/rand`, результат 32 байта, PHC-строка, сравнение за постоянное время, семафор на 4 одновременных вычисления (research R3)
- [X] T006 [P] Написать `server/internal/bootstrap/bootstrap_test.go`: создание пароля базы, сертификата и ключа в пустой папке; повторный запуск не меняет секреты в томе, но заново копирует открытый сертификат, в том числе поверх устаревшего файла в `certsDir`; права `server.key` — `0600`, у владельца-сервера, а `db_password` — `0644`, чтобы его читали и сервер, и Postgres (uid 70 в Alpine); сертификат содержит `localhost`, `server`, `127.0.0.1`, срок 1 год, и с ним поднимается TLS-соединение
- [X] T007 Реализовать в `server/internal/bootstrap/bootstrap.go` `Init(secretsDir, certsDir string, owner int)`: пароль из `crypto/rand`, самоподписанный сертификат ECDSA P-256; существующие секреты в томе не менять; открытый сертификат копировать в `certsDir` всегда, перезаписывая старый файл; права `server.key` — `0600`, `db_password` — `0644` (том подключён только к `init`, `postgres` и `server`); смена владельца `server.key` на пользователя сервера, где система это поддерживает
- [X] T008 Написать `server/internal/postgres/migrate_test.go`: миграции применяются на пустой базе, повторный запуск ничего не меняет, параллельный запуск безопасен; общий помощник `server/internal/postgres/testdb_test.go` берёт адрес из `CHAT_TEST_POSTGRES_DSN` и создаёт для каждого теста отдельную временную схему, удаляя её в конце. Без переменной тест явно пропускается, а при `CHAT_REQUIRE_POSTGRES=1` падает; под `testing.Short()` пропускается
- [X] T009 Реализовать в `server/internal/postgres/postgres.go` подключение `pgxpool` и таймаут запроса 3 с, а в `server/internal/postgres/migrate.go` встроенные SQL-миграции и таблицу `schema_migrations` под `pg_advisory_lock`. Схема из `data-model.md`: `users` с уникальным индексом по `login_norm`; `sessions` с `token_hash` как первичным ключом и `ON DELETE CASCADE`; `contact_requests` с проверкой «`sender_id <> recipient_id`»; `contacts` с проверкой «`user_low < user_high`». Функция `Ping` для проверки готовности; ошибки подключения превращаются в `store.ErrUnavailable`
- [X] T010 [P] Написать `server/internal/rpc/errors_test.go`: каждая ошибка сервисов превращается в ожидаемый код gRPC и причину `ErrorInfo` с доменом `cryptochat`; неизвестная ошибка даёт `Internal` без текста исходной ошибки
- [X] T011 Реализовать в `server/internal/rpc/errors.go` преобразование ошибок и коды причин из research R7, а в `server/internal/rpc/server.go` `NewServer(tlsConfig, deps)`: минимум TLS 1.3, `MaxRecvMsgSize` 64 КиБ, перехватчики журнала (`slog`: метод, код, длительность, без тел и метаданных) и восстановления после паники (`Internal`, сервер продолжает работу), gRPC Health

**Checkpoint**: `go test -short ./internal/...` зелёный; миграции проходят на настоящем Postgres.

---

## Phase 3: User Story 1 - Регистрация и вход (Priority: P1) 🎯 MVP

**Goal**: регистрация, вход, сессии и выход через gRPC.

**Independent Test**: `go test ./internal/accounts ./internal/rpc -v`: полный цикл через gRPC в памяти, с настоящим TLS.

### Tests for User Story 1

- [X] T012 [P] [US1] `server/internal/accounts/accounts_test.go` на `memstore`: логин «3–32 символа `[A-Za-z0-9_.-]`» и пароль «от 8 до 128 байт», проверка границ, символы вне латиницы и пробелы в пароле; слишком длинный пароль отвергается без вычисления хеша; занятый логин и другой регистр дают `ErrLoginTaken`, запись не меняется, написание сохраняется; вход; одинаковая ошибка при неверном пароле и неизвестном логине; срок сессии 24 часа через подставляемые часы; просроченная и завершённая сессия отвергаются; повторный выход даёт ошибку; вход с логином или паролем вне правил даёт ту же ошибку неверных данных, что и неверный пароль, а пароль длиннее 128 байт не хешируется; при входе удаляются просроченные сессии; в хранилище только SHA-256 сессии; под `!testing.Short()` среднее время двух видов неудачного входа различается не больше чем на 20% (SC-010)
- [X] T013 [P] [US1] `server/internal/postgres/accounts_test.go` при `CHAT_TEST_POSTGRES_DSN`: 100 одновременных регистраций одного логина, в том числе в разном регистре, успешна ровно одна, остальные `ErrLoginTaken` (SC-001); сессии; недоступная база даёт `store.ErrUnavailable`
- [X] T014 [P] [US1] `server/internal/rpc/accounts_test.go` через `bufconn` с настоящим TLS и `memstore`: регистрация, вход, защищённая операция, выход; коды и причины ошибок; ответы на неверный пароль и неизвестный логин совпадают; без сессии, с мусорной и с просроченной сессией `UNAUTHENTICATED`; запрос больше 64 КиБ отвергается; паника в обработчике даёт `Internal`, следующий запрос работает; перехваченный журнал не содержит пароля и сессии; отмена запроса клиентом не ломает сервер

### Implementation for User Story 1

- [X] T015 [US1] Реализовать в `server/internal/store/memstore/memstore.go` часть `AccountStore` на памяти с блокировкой
- [X] T016 [US1] Реализовать в `server/internal/accounts/accounts.go` `Service`: `Register`, `Login`, `Logout`, `Authenticate`. Сессия — 32 байта из `crypto/rand` в base64url, в хранилище SHA-256, срок 24 часа. Для неизвестного логина и логина вне правил проверка идёт по фиктивному хешу, вычисленному при создании сервиса; пароль длиннее 128 байт сразу даёт ошибку неверных данных, без вычисления хеша (research R3, R4)
- [X] T017 [US1] Реализовать в `server/internal/postgres/accounts.go` `AccountStore`: ошибка `23505` превращается в `store.ErrLoginTaken`
- [X] T018 [US1] Реализовать в `server/internal/rpc/auth.go` перехватчик авторизации по метаданным `authorization: Bearer …` и передачу пользователя через `context`, а в `server/internal/rpc/accounts.go` обработчики `Accounts`; `Register` и `Login` доступны без сессии

**Checkpoint**: регистрация, вход и выход работают через gRPC; `Authenticate` готов для контактов.

---

## Phase 4: User Story 2 - Управление контактами (Priority: P1)

**Goal**: запросы, принятие, отклонение, удаление и списки.

**Independent Test**: `go test ./internal/contacts ./internal/rpc -v`: два пользователя проходят весь цикл.

**Зависит от**: US1 (авторизация и пользователи).

### Tests for User Story 2

- [X] T019 [P] [US2] `server/internal/contacts/contacts_test.go` на `memstore`: переходы из `data-model.md`: запрос появляется во входящих и исходящих; принятие создаёт контакт у обоих; отклонение убирает запрос, можно отправить новый; удаление действует у обоих, можно отправить новый; `SELF_REQUEST`, `USER_NOT_FOUND`, `ALREADY_CONTACT`, `DUPLICATE_REQUEST`, `REQUEST_NOT_FOUND`, `CONTACT_NOT_FOUND`; встречный запрос даёт контакт и `became_contact`; посторонний не может принять, отклонить или удалить чужое, данные не меняются; логин ищется без учёта регистра, а показывается как при регистрации
- [X] T020 [P] [US2] `server/internal/postgres/contacts_test.go` при `CHAT_TEST_POSTGRES_DSN`: те же переходы; 100 одновременных операций над одной парой: встречные запросы, принятие и отклонение, удаление: нет дублей, взаимных блокировок и нарушений ограничений (SC-004)
- [X] T021 [P] [US2] `server/internal/rpc/contacts_test.go` через `bufconn`: все методы требуют сессию; коды и причины ошибок; пользователь берётся только из сессии; списки контактов, входящих и исходящих запросов

### Implementation for User Story 2

- [X] T022 [US2] Дополнить `server/internal/store/memstore/memstore.go` частью `ContactStore`
- [X] T023 [US2] Реализовать в `server/internal/contacts/contacts.go` `Service`: `SendRequest`, `AcceptRequest`, `RejectRequest`, `RemoveContact`, `List`
- [X] T024 [US2] Реализовать в `server/internal/postgres/contacts.go` `ContactStore`: каждая операция — одна транзакция; сначала `SELECT … FOR UPDATE` обеих строк `users` по возрастанию `id`; контакт хранится парой «`user_low < user_high`» (research R5)
- [X] T025 [US2] Реализовать в `server/internal/rpc/contacts.go` обработчики `Contacts`

**Checkpoint**: контакты работают через gRPC и согласованы при одновременных запросах.

---

## Phase 5: User Story 3 - Защищённый запуск сервера (Priority: P1)

**Goal**: запуск одной командой на чистой машине, тестовый клиент, сквозная проверка.

**Independent Test**: `go test -tags e2e ./test/e2e -v -timeout 15m` проходит на настоящем Compose.

**Зависит от**: US1 и US2.

### Tests for User Story 3

- [X] T026 [P] [US3] `server/cmd/server/config_test.go`: значения по умолчанию и переопределение переменных `CHAT_*` из `contracts/deployment.md`; отсутствующий файл секрета даёт понятную ошибку, не содержащую значений секретов
- [X] T027 [US3] `server/test/e2e/e2e_test.go` с тегом `e2e`: поднимает отдельный проект Compose с именем `cryptochat-e2e`, своим портом и папкой сертификатов через `CHAT_PUBLIC_PORT` и `CHAT_CERTS_DIR`, снимает его в конце. Проверяет: запуск и время до `healthy` (SC-005); сценарий регистрации и контактов через `chatcli`; поиск использованных паролей и сессий в `pg_dump` и `docker compose logs`: 0 совпадений (SC-002); соединение без TLS не проходит (SC-007); данные и вход после перезапуска (SC-006); остановка `postgres`: ошибка `STORE_UNAVAILABLE` не дольше 5 с, сервер не завершается, работает не позже 30 с после возврата базы (SC-008); время входа и операций (SC-009); штатное завершение по сигналу с кодом 0

### Implementation for User Story 3

- [X] T028 [US3] Реализовать в `server/cmd/server/config.go` чтение переменных `CHAT_*`, а в `server/cmd/server/main.go` подкоманды `server`, `server init`, `server healthcheck` из `contracts/deployment.md`: сборка сервисов, gRPC Health с проверкой базы раз в 2 с, ожидание базы при запуске, `GracefulStop` с пределом 10 с по сигналу
- [X] T029 [P] [US3] Реализовать в `deploy/server.Dockerfile` многоэтапную сборку: `golang:1.23-alpine`, контекст — папка `project`, `GOWORK=off`, сборка из `server/`, итоговый образ `gcr.io/distroless/static-debian12:nonroot`. Создать в `project` файл `.dockerignore`, исключающий `.claude`, `.specify`, `specs`, `report`, `docs`, `deploy/certs`, `*.tmp`, `*.exe`, чтобы контекст сборки был минимальным и без файлов сессий и сертификатов
- [X] T030 [US3] Реализовать в `deploy/compose.yaml` контейнеры `init` (запуск от root только для создания файлов, затем завершение), `postgres:16-alpine` с `POSTGRES_PASSWORD_FILE`, томом `pgdata` и без открытого порта, `server` с проверкой `server healthcheck`, запуском после `init` и готовности базы и портом `${CHAT_PUBLIC_PORT:-8443}`. Папка сертификатов монтируется из `${CHAT_CERTS_DIR:-./certs}`. Дополнить `specs/003-server-accounts/contracts/deployment.md` этими двумя переменными
- [X] T031 [US3] Реализовать в `server/cmd/chatcli/main.go` тестовый клиент по `contracts/deployment.md`: команды `register`, `login`, `logout`, `contact-request`, `contact-accept`, `contact-reject`, `contact-remove`, `contacts`; пароль из stdin, с обрезкой одного завершающего `\n` или `\r\n`; сессия в файле с правами `0600`; доверие только указанному сертификату; при ошибке печать причины из `ErrorInfo` и код завершения 1
- [X] T032 [US3] Перемерить `argon2id` в контейнере и записать результат в `specs/003-server-accounts/research.md`, раздел R3; при превышении SC-009 скорректировать параметры, не опускаясь ниже минимума OWASP `t = 2, m = 19 МиБ`, и обновить R3.

**Checkpoint**: запуск одной командой работает, сквозной тест зелёный.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: проверки перед завершением фичи

- [X] T033 Прогнать агента `test-runner` и все команды из `specs/003-server-accounts/quickstart.md`, включая `go test -short -race ./...`, сквозной тест и `go test ./internal/postgres -v` с `CHAT_TEST_POSTGRES_DSN` и `CHAT_REQUIRE_POSTGRES=1`, чтобы SC-001 и SC-004 не могли быть тихо пропущены; для `-race` в Git Bash `/c/msys64/ucrt64/bin` должен стоять первым в PATH; сохранить вывод в `report/runs/003-test-full.txt`
- [X] T034 Проверить зависимости: `go list -deps ./...` в `server` не содержит `cryptochat/core`; из `crypto/*` прямо импортируются только разрешённые пакеты; записать результат в `report/runs/003-imports.txt`
- [X] T035 [P] Прогнать агента `crypto-checker` по паролям, сессиям, TLS и журналам, исправить расхождения и записать результат в `report/runs/003-crypto-check.md`
- [X] T036 [P] Прогнать агента `design-reviewer` по `server` и `deploy` с учётом пунктов 3.1–3.3, исправить замечания и записать результат в `report/runs/003-design-review.md`
- [X] T037 Дополнить `report/note.md` агентом `report-writer`: архитектура сервера, развёртывание, хранение паролей и сессий, ограничения из research R10, только по фактическим прогонам
- [X] T038 Проверить каждый критерий SC-001…SC-010 и требование FR-001…FR-021 по фактическим результатам, записать таблицу в `report/runs/003-acceptance.md`

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: без зависимостей; T002 требует Docker
- **Foundational (Phase 2)**: после Setup
- **US1 (Phase 3)**: после Phase 2
- **US2 (Phase 4)**: после US1
- **US3 (Phase 5)**: после US1 и US2
- **Polish (Phase 6)**: после всех историй

### User Story Dependencies

```text
Setup → Foundational → US1 → US2 → US3 → Polish
```

### Within Each User Story

- Тесты пишутся первыми и сначала падают
- Порядок реализации: `store` → сервис → `postgres` → `rpc`
- История считается готовой после своего checkpoint

### Parallel Opportunities

- T004, T006 и T010 — в разных пакетах
- Тесты с [P] внутри истории — в разных файлах
- T029 — отдельно от кода сервера
- T035 и T036

---

## Parallel Example: User Story 1

```text
T012: server/internal/accounts/accounts_test.go
T013: server/internal/postgres/accounts_test.go
T014: server/internal/rpc/accounts_test.go
```

---

## Implementation Strategy

### MVP First

1. Setup и Foundational: API, пароли, TLS, миграции.
2. US1: регистрация и вход через gRPC.
3. **Остановиться и проверить** на gRPC в памяти и настоящем Postgres.

### Incremental Delivery

1. US1 → учётные записи и сессии
2. US2 → контакты, от которых зависят чаты
3. US3 → запуск одной командой и сквозная проверка
4. Polish → приёмка и записка

### Notes

- Реализацию запускать в сессии, открытой прямо в `project`, иначе хуки не работают.
- Перед реализацией запустить Docker Desktop.
