# Проверка криптографии: 003-server-accounts (T035)

Способ проверки: чтение кода и `go test -short` пакетов `password`, `accounts`, `bootstrap`, `rpc`. Работа в контейнере, Postgres и `-race` проверены отдельно, см. `003-test-full.txt`.

| Проверка | Результат | Доказательство |
|---|---|---|
| argon2id: t=3, m=64 МиБ, p=4, ключ 32 байта | соответствует | `internal/password/password.go`, `TestHashAndVerify` |
| Соль 16 байт из `crypto/rand`, новая на каждый хеш | соответствует | `password.go`; длина проверяется в `TestHashAndVerify` (добавлено по итогам проверки) |
| PHC-строка, строгий разбор | соответствует | `password.parse`, `TestVerifyMalformed` |
| Сравнение за постоянное время | соответствует | `subtle.ConstantTimeCompare` в `password.go` |
| Фиктивный хеш для неизвестного логина | соответствует | `accounts.go`, `TestLoginErrorsAreEqual`, `TestLoginTiming` |
| Ограничение одновременных вычислений: 4 | соответствует | `TestConcurrencyLimit`, `TestCanceledContext` |
| Токен сессии: 32 байта из `crypto/rand` | соответствует | `accounts.go` |
| В базе только SHA-256 токена | соответствует | `hashToken`, `migrations/001_init.sql`, `postgres/accounts.go` |
| Срок сессии 24 ч, отзыв при выходе, просроченные отвергаются | соответствует | `TestSessionLifecycle`, `TestSessionRejected` |
| Только TLS 1.3 | соответствует | `bootstrap.go`, `TestTLSHandshake`, сквозной тест SC-007 |
| Клиент доверяет только указанному сертификату | соответствует | `ClientTLS`, `TestTLSHandshake` |
| SAN localhost, server, 127.0.0.1; срок 1 год | соответствует | `TestInitCreatesSecrets` |
| Ключ `0600`, не попадает в папку клиента | соответствует | `bootstrap.go`, `TestInitCreatesSecrets`; проверка прав пропускается на Windows, в контейнере ключ читает сервер без root (сквозной тест) |
| Журналы и ошибки без паролей, токенов, хешей, DSN | соответствует | `logInterceptor`, `postgres.Open`, `config.databaseURL`, сквозной тест SC-002 |
| Нет запрещённых криптобиблиотек | соответствует | `003-imports.txt` |
| Сервер не зависит от `cryptochat/core` | соответствует | `003-imports.txt` |
| Конкурентная регистрация | соответствует | `TestConcurrentRegistration` на Postgres, `003-test-full.txt` |

## Расхождения

Критичных и серьёзных нет.

| Замечание | Серьёзность | Решение |
|---|---|---|
| Верхние пределы разбора хеша велики: 1 ГиБ памяти на попытку при подмене строки в базе | низкая | исправлено: 256 МиБ, t и p не больше 16 |
| Длина соли не проверялась тестом | низкая | исправлено |
| Комментарий о содержимом стека при панике сильнее гарантии | информационно | исправлено |
| `db_password` с правами `0644` | низкая | оставлено, описано в research R10 |
| Подключение к Postgres с `sslmode=disable` | низкая | оставлено, описано в research R10 |
| `TestLoginTiming` чувствителен к нагрузке | низкая | оставлено: тест прошёл во всех полных прогонах |
