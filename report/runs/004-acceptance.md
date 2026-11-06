# Приёмка: 004-secret-chats

Результаты взяты из `004-test-full.txt` (прогон 2026-11-06, итог `failed=0`, строк SKIP и DATA RACE нет) и `004-imports.txt`.

## Критерии успеха

| Критерий | Результат | Доказательство |
|---|---|---|
| SC-001: все сценарии чатов | выполнено | `chatstest` на памяти и Postgres, `internal/rpc`, `TestChatsEndToEnd` через `chatcli` |
| SC-002: значения проходят без изменений, ключи совпадают | выполнено | `TestKeyAgreementThroughServer`: 6 подтестов, MAGENTA и RC6, 128, 192, 256 бит |
| SC-003: посторонний неотличим от несуществующего чата | выполнено | `TestStrangerSeesNotFound`: 6 методов, ответы совпадают |
| SC-004: 100 одновременных операций | выполнено | `TestConcurrentChatOperations` на Postgres |
| SC-005: значений нет в журналах и в базе после закрытия | выполнено | `TestChatsEndToEnd`: поиск в `pg_dump` и журналах контейнеров |
| SC-006: данные после перезапуска | выполнено | `TestChatsEndToEnd`: `down`, затем `up` без удаления томов |
| SC-007: операция не дольше 200 мс | выполнено: от 4,1 до 24,8 мс | `TestChatsEndToEnd`, один замер каждой операции на этой машине |
| SC-008: ошибка не дольше 5 с, восстановление за 30 с | выполнено: 3,7 мс и 45,8 мс | `TestChatsEndToEnd` |
| SC-009: нет криптоядра в сервере, критерии `003` выполняются | выполнено | `004-imports.txt`, `TestEndToEnd` |

## Функциональные требования

| Требования | Результат | Доказательство |
|---|---|---|
| FR-001…FR-008: жизненный цикл | выполнено | `chatstest`, `internal/chats`, `TestChatLifecycle`, `TestCreateChatErrors`, `TestEnumsMatchValidation` |
| FR-009…FR-011: подключение | выполнено | `chatstest`, `TestConnectDisconnect` |
| FR-012…FR-016: значения | выполнено | `chatstest`, `internal/keyexchange`, `TestKeyExchange`, `TestCloseErasesValues` |
| FR-017: нет секрета и ключа в API | выполнено | `chats.proto`, `004-crypto-check.md`, `TestClientRejectsBadValues` |
| FR-018, FR-019: сессия и посторонние | выполнено | `TestChatMethodsRequireSession`, `TestStrangerSeesNotFound` |
| FR-020: одновременность и перезапуск | выполнено | гонки в `postgres/chats_test.go`, SC-006 |
| FR-021: ошибки без частичных изменений | выполнено | `TestCreateChatRollsBack`, `TestChatErrorMapping`, `TestChatStoreUnavailable`, SC-008 |
| FR-022: журналы | выполнено | `TestKeyExchange`, SC-005 |
| FR-023: сервер без шифров | выполнено | `004-imports.txt` |
| FR-024: автоматическая миграция | выполнено | `TestMigrateUpgrade` |
| FR-025: тестовый клиент | выполнено | `cmd/chatcli/chats.go`, `TestChatsEndToEnd`, сценарий quickstart |

## Что не проверено

- Замеры времени сделаны один раз на этой машине, на другой значения будут отличаться.
- Шифры MAGENTA и RC6 не участвуют: сервер хранит выбор как параметр, а SC-002 проверяет только выработку ключа. Сами шифры появятся в `002-ciphers`.
- Передача сообщений и очереди относятся к `005`; входные данные для неё собраны в исследование решений, R10.
- Ограничения реализации перечислены в исследование решений, R9.
