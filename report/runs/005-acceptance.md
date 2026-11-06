# Приёмка: доставка шифротекста через брокер

Способ проверки: таблица составлена по фактическим результатам `005-test-full.txt` (прогон 2026-11-06, итог `failed=0`, строк SKIP и DATA RACE нет) и `005-imports.txt` (итог `failed=0`). Замеры относятся к одному прогону на этой машине с Docker Desktop. Шифры MAGENTA и RC6 не реализованы, поэтому сквозная доставка проверена на тестовом шифре. Отдельные проверки: `005-crypto-check.md`, `005-design-review.md`.

## Критерии успеха

| Критерий | Результат | Доказательство |
|---|---|---|
| SC-001: сценарии отправки, получения, подтверждения и ошибок | выполнено | `internal/delivery`, `internal/rpc`; `TestDeliveryEndToEnd` в `server/test/e2e` и ручной сценарий через `chatcli send` и `recv` |
| SC-002: текст и файл восстанавливаются байт в байт | выполнено: текст, файл 3 145 728 байт, 64 фрагмента | `TestDeliveryEndToEnd` в `integration`, настоящий брокер, тестовый шифр |
| SC-003: содержимое брокера равно отправленному, метки нет | выполнено: 2 сообщения совпали; метка не найдена на диске брокера, в журналах и базе | там же: чтение очереди через опубликованный только в тесте порт; файлы брокера проверены поиском, ещё не сброшенные на диск данные таким поиском не видны, их покрывает сверка тел сообщений |
| SC-004: после подтверждения пусто, неподтверждённое приходит снова | выполнено: повторных выдач 16, повторов при сборке 10, результат совпал | там же; `TestUnackedReturns`, `TestAckAndRedelivery` |
| SC-005: очереди исчезают за 5 с, а после восстановления за 30 с | выполнено: закрытие 1,63 с, удаление контакта 1,65 с, закрытие при остановленном брокере 10,7 с после запуска брокера | `TestDeliveryEndToEnd` в `server/test/e2e`. Перезапуск сервера до удаления проверен на памяти: `TestCleanupAfterRestart`, `TestLateQueueSurvivesRestart` |
| SC-006: поток завершается за 3 с с верной причиной | выполнено: выход 2,00 с, отключение 2,00 с, закрытие 0,59 с, удаление контакта 0,58 с | `TestStreamEnds` с настоящей сверкой раз в 2 с; обрыв: `TestClientGoneDisconnects` и сквозной тест, граница 3 с |
| SC-007: посторонний неотличим от несуществующего чата | выполнено | `TestStrangerSameAnswer`: оба метода, ответы совпадают |
| SC-008: наибольший фрагмент принят, на байт больше отвергнут | выполнено: наибольший фрагмент 50 576 байт, запас до предела 10 864 байта; 61 440 байт приняты, 61 441 байт дают `FRAGMENT_TOO_LARGE` | `TestDeliveryEndToEnd` в `integration`, `TestLargestFragmentFits`, `TestMaxWireSize`, `TestSendChecksInOrder` |
| SC-009: отправка до 200 мс, доставка до 1 с | выполнено: наибольшие из 20 замеров 47,7 мс и 47,7 мс для фрагмента 53 312 байт | `TestDeliveryEndToEnd` в `server/test/e2e` |
| SC-010: ошибка до 5 с, восстановление до 30 с | выполнено: ошибка 4,9 мс, восстановление 9,7 с, контейнер сервера не перезапускался | там же. `NOT_SERVING` замерено от момента, когда команда `stop` уже вернулась: 1,6 мс; сама команда занимает несколько секунд, журнал показывает смену состояния не позже следующей проверки раз в 2 с |
| SC-011: ожидающие фрагменты после перезапусков | выполнено | там же: `restart rabbitmq`, пересоздание контейнера без удаления тома, `restart server` |
| SC-012: заполненная очередь, не больше 16 на поток | выполнено | `TestQueueFull`, `TestQueueFullThroughServer`, `TestPrefetchLimit`; набор поведения брокера на RabbitMQ проверяет отказ при заполнении с меньшим пределом. На полных 4 096 сообщениях сквозной проверки нет, аргументы очереди сверены через `list_queues` |
| SC-013: нет криптоядра и шифров, нет портов брокера, прежние критерии | выполнено | `005-imports.txt`; `compose port` печатает `invalid IP:0`, в `compose ps` нет `->`; `TestEndToEnd`, `TestChatsEndToEnd`, `TestKeyAgreementThroughServer`, `TestClientRejectsBadValues` и тесты `core` проходят |

## Функциональные требования

| Требования | Результат | Доказательство |
|---|---|---|
| FR-001, FR-002: отправка без участия собеседника | выполнено | `TestSendChecksInOrder`, `TestSendToDisconnectedPeer`, `TestSendAndReceive` |
| FR-003: producer и consumer на сервере | выполнено | `internal/rabbit`, сквозные тесты; у клиента нет обращения к брокеру |
| FR-004: очередь на чат и получателя | выполнено | `TestBothDirections`, `TestSendBodyAndQueues` |
| FR-005: открытие потока подключает | выполнено | `TestOpenBeforeReadyConnects`, `TestOpenRules` |
| FR-006: номер доставки и предел | выполнено | `TestPrefetchLimit`, набор `brokertest` на обоих брокерах |
| FR-007: хотя бы одна доставка | выполнено | `TestUnackedReturns`, SC-004 |
| FR-008: сохранность при перезапусках | выполнено | SC-011 |
| FR-009, FR-010: только фрагмент, непрозрачные байты | выполнено | SC-003, `TestSendBodyAndQueues`, `005-imports.txt` |
| FR-011: формат определён и описан | выполнено | `proto/cryptochat/v1/delivery.proto`, проверка свойств сообщений в SC-003, раздел «Формат сериализации» записки |
| FR-012: размеры | выполнено | SC-008, `TestFragmentFitsTransport`, `TestSendTooLargeForTransport` |
| FR-013: подтверждение брокеру только после клиента | выполнено | `TestAckReachesBrokerOnlyFromClient` |
| FR-014: содержимое не хранится | выполнено | `TestDeliveryLogHasNoContent`, SC-003 |
| FR-015, FR-016: надёжное удаление очередей | выполнено | `internal/delivery/cleanup_test.go`, `chatstest`, `TestCleanupDoesNotLoseChats`, SC-005 |
| FR-017: гонка отправки с закрытием | выполнено | `TestSendRaceCloseDuringEnsure`, `TestSendRaceCloseDuringPublish`, `TestSendRaceCloseAfterPublish`, `TestSendRaceDeleteFailureRetried`, `TestLateQueueSurvivesRestart`, `TestOpenRaceWithClose` |
| FR-018: журнал и защита потоков | выполнено | `TestStreamInterceptorsLogAndRecover`, `TestStreamPanicThroughServer`, `TestWriterPanicIsRecovered` |
| FR-019: состояние участника завершает поток | выполнено | `TestStreamEndRules`, `TestStreamEnds`, `TestSessionExpiryEndsStream` |
| FR-020, FR-021: подключение и один поток | выполнено | `TestStreamEndRules`, `TestOpenFailureChangesNothing`, `TestCloseRacesWithOpen`, `TestSecondStreamReplaces`, `TestInvalidSequence`, `TestShutdownEndsStreams` |
| FR-022: ограничение очереди | выполнено | SC-012 |
| FR-023: срок подтверждения | выполнено | `TestAckTimeoutRule`, `TestAckTimeoutOldestWins`, `TestAckTimeoutCountsFromIssue`, `TestAckTimeout`, `TestNonReadingClient` |
| FR-024: лёгкая проверка | выполнено | `TestSendIsLight`, `TestQueueCleanupUsesIndex` для запроса обхода, `DeliveryState` одним запросом |
| FR-025: брокер в развёртывании | выполнено | `deploy/compose.yaml`, `TestInitBrokerSecrets`, SC-013 |
| FR-026: переживание сбоя брокера | выполнено | `TestStartWithoutBroker`, `TestConnectionLoss`, `TestBrokerDownThroughServer`, SC-010 |
| FR-027: готовность учитывает брокер | выполнено | `TestReadyAll`, `TestHealthFollowsStore`, SC-010 |
| FR-028: понятные ошибки без внутренних текстов | выполнено | `TestDeliveryErrorMapping`, `TestBrokerURL`, `TestConnectionLoss` |
| FR-029: сессия и посторонние | выполнено | `TestDeliveryRequiresSession`, SC-007 |
| FR-030: обновление схемы | выполнено | `TestMigrateQueueCleanup` |
| FR-031: тестовый клиент | выполнено | `cmd/chatcli/delivery_test.go`, сквозной тест, ручной сценарий |
| FR-032: сквозной тест | выполнено на тестовом шифре | SC-002, SC-003 |

## Что не проверено

- Настоящие шифры MAGENTA и RC6: они не реализованы.
- Полуоткрытое соединение и срабатывание keepalive: параметры заданы, отдельного теста нет.
- Блокировка соединения брокером при нехватке памяти или диска: предел публикации работает на контексте, настоящей блокировки в тестах нет.
- Сквозное заполнение очереди на настоящем брокере до рабочих пределов 64 МиБ и 4 096 сообщений.
- Настоящий срок подтверждения 60 с: сценарии идут с короткими сроками.
- Отсутствие шифротекста на диске брокера после подтверждения: поиск идёт по открытой метке, а RabbitMQ не затирает байты сразу. Ограничение описано в записке.
