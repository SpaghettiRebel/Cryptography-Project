# Проверка криптографии: 004-secret-chats (T033)

Проверял агент `crypto-checker` чтением кода и быстрыми тестами `chats`, `keyexchange`, `memstore`. Postgres, gRPC, `core/dh`, сквозной тест и `integration` запущены отдельно, см. `004-test-full.txt`.

| Проверка | Результат | Доказательство |
|---|---|---|
| В API нет общего секрета, сеансового ключа и закрытых значений | соответствует | `chats.proto`: ключевые данные только в `public_value`; `store.ChatParticipant` хранит лишь признак наличия |
| Сервер не импортирует `cryptochat/core`, нет кода протокола и шифров | соответствует | `004-imports.txt`; `keyexchange` проверяет только длину |
| Значения вне журналов и текстов ошибок | соответствует | `logInterceptor`, `mapError`, `toStatus`; `TestKeyExchange`, `TestChatsEndToEnd` |
| Значения удаляются при закрытии | соответствует | `postgres/chats.go`, `memstore/chats.go`; `chatstest`, `TestCloseErasesValues`, `TestChatsEndToEnd` (поиск в `pg_dump`) |
| Значения удаляются при удалении контакта | соответствует | `closePairChats`; `chatstest`, `TestCloseErasesValues`, `TestRemoveContactRacesWithChatOperations` |
| Формат значения: ровно 256 байт big-endian с ведущими нулями | соответствует | `core/dh/encoding.go`, `TestPublicBytesFixedLength` |
| `ParsePublic` проверяет длину и диапазон `[2, p − 2]` | соответствует | `TestParsePublicRejects`, `TestParsePublicAcceptsBounds` (добавлен по итогам проверки) |
| Идентификатор чата: 16 байт из `crypto/rand`, ошибка источника не теряется | соответствует | `chats.go`, `TestRandomFailureIsReturned`, `TestIDs` |
| Ключи сторон совпадают через настоящий сервер для всех длин | соответствует | `TestKeyAgreementThroughServer`: MAGENTA и RC6, 128, 192, 256 бит |
| Значение проверяется на принимающей стороне | соответствует | `ParsePublic` в `sessionKey`; `TestClientRejectsBadValues` (добавлен по итогам проверки): 0, 1 и `p − 1` проходят через сервер, клиент их отвергает |

## Расхождения

Критичных и серьёзных нет.

| Замечание | Серьёзность | Решение |
|---|---|---|
| В research R4 фраза о подменённом значении создавала ложное чувство защиты: допустимое значение, подставленное сервером, отличить нельзя | средняя | исправлено, research R4 |
| В research R9 не хватало посредника с разными значениями, сверки отпечатков, сокрытия значений сервером | низкая | исправлено |
| Нет негативного сквозного теста клиента | низкая | исправлено: `TestClientRejectsBadValues` |
| Не проверялись граничные допустимые значения 2 и `p − 2` | низкая | исправлено: `TestParsePublicAcceptsBounds` |
| Поиск в журналах не учитывает base64 | очень низкая | оставлено: журнал по построению не содержит тел запросов |
| `memstore.CreateChat` затирает чат при совпадении идентификатора | очень низкая | оставлено: только для тестов, совпадение практически невозможно |
