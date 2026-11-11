Способ проверки: сопоставление каждого требования и критерия переписки клиента с фактическими результатами полного прогона `report/runs/007-test-full.txt` (ИТОГ: failed=0, 2026-11-11) и проверки зависимостей `report/runs/007-imports.txt` (ИТОГ: failed=0). Тесты переписки выполнялись со сборкой `devcipher` (тестовый шифр вместо MAGENTA и RC6). Ручная проверка двумя окнами (стандартные диалоги, показ картинки, отзывчивость, удобство) не выполнялась: такие пункты отмечены «проверяется вручную».

# Приёмка: клиент — сообщения и файлы

Обозначения тестов: `delivery/…` — модульные тесты пакета `client/internal/delivery` на подделке сервера; `service/…` — тесты сервиса; `e2e/…` — сквозные тесты `client/internal/e2e` на настоящем сервере, брокере и базе (Docker Compose); `UI/…` — тесты интерфейса Vitest. Все перечисленные тесты зелёные в `007-test-full.txt` с двумя уточнениями: проверка файлов `delivery/TestFiles` во всех 28 сочетаниях режима и набивки — отдельный шаг после полного прогона (раздел «дополнение после прогона»; в полном прогоне с `-short` проверяется только CBC); тесты исправлений по независимым проверкам — в разделе «повтор после исправлений по проверкам» того же файла. «Выполнено на тестовом шифре» означает, что сценарий проверен со сборкой `devcipher`, где вместо MAGENTA и RC6 работает тестовый шифр; с настоящими алгоритмами он не проверялся.

## Требования

| требование | способ проверки | результат |
|---|---|---|
| FR-001 ввод текста и выбор файла стандартным диалогом | UI/Composer.test (поле, Enter, «Файл…» вызывает SendFile); фасад `SendFile` открывает `runtime.OpenFileDialog` | выполнено; сам стандартный диалог — проверяется вручную |
| FR-002 шифрование на устройстве ключом и параметрами чата | delivery/TestTextAllModes (28 сочетаний, длины ключа 128/192/256), delivery/TestFiles (28 сочетаний), e2e/TestTextMessaging (28 сочетаний в обе стороны) | выполнено на тестовом шифре |
| FR-003 свой IV из криптостойкого источника на каждую часть | delivery/TestTextAllModes: IV 16 байт и попарно различны у режимов с IV, у ECB IV нет; IV создаёт `core/message` из `crypto/rand` | выполнено на тестовом шифре; независимая проверка криптографии — за координатором |
| FR-004 многопоточность, равенство с однопоточной обработкой | delivery/TestParallelAssembly (WriteAt одновременно из нескольких горутин), delivery/TestSingleAndMultiThreaded (GOMAXPROCS 1 и по умолчанию) | выполнено на тестовом шифре |
| FR-005 имя, тип и размер только внутри шифротекста | delivery/TestFiles (метка имени не найдена в частях), e2e/TestFileMessaging и TestQueueFullOfflineReceiver (имена и содержимое не найдены в журналах сервера, pg_dump и файлах брокера) | выполнено на тестовом шифре |
| FR-006 отправка доступна только в активном чате с ключом, подключением и алгоритмом | service/TestSendBlocks (KEY_NOT_READY, NOT_CONNECTED, RECEIVING_ELSEWHERE), service/TestClosedChatHistory (CHAT_CLOSED), delivery/TestReleaseBuild и service/TestTextValidation без тега (ALGORITHM_UNAVAILABLE), UI/Composer.test (неактивные поле и кнопки с подсказкой) | выполнено: ALGORITHM_UNAVAILABLE — в сдаваемой сборке, остальные причины — на тестовом шифре |
| FR-007 пределы 64 КиБ и 256 МиБ, пустой текст | service/TestTextValidation (EMPTY_TEXT, TEXT_TOO_LONG при 65 537 байт, FILE_TOO_LARGE, FILE_READ, ни одного SendFragment), service/TestFileLimitExact (ровно 256 МиБ принимается), delivery/payload_test | выполнено |
| FR-008 передача по кнопке в фоне, сообщения чата по порядку | delivery/TestSameTextTwiceAndOrder (части уходят в порядке номеров сообщений) | выполнено на тестовом шифре |
| FR-009 фоновый приём, в том числе после входа | e2e/TestTextMessaging (получатель закрыт, после входа текст доставлен), service/TestSendBlocks (приём открывается по желанию после синхронизации) | выполнено на тестовом шифре |
| FR-010 подтверждение только после записи | delivery/TestTextAllModes и TestDisconnectAndParallelCancel (в момент каждого подтверждения в базе получателя записано не меньше частей), delivery/TestStoreFailureAndPanic (ошибка записи — без подтверждения) | выполнено на тестовом шифре |
| FR-011 сборка по номерам, повторы не дублируют | delivery/TestOrderAndDuplicates (обратный и случайный порядок, каждая часть дважды, повторная выдача), delivery/TestBadFragments (повтор целого сообщения), delivery/TestReceiverRestart | выполнено на тестовом шифре |
| FR-012 само возобновление приёма | delivery/TestStreamEndings (SERVER_BUSY, SERVER_UNAVAILABLE, ACK_TIMEOUT, INTERNAL — переоткрытие с паузой не больше 5 с), e2e/TestServerRestartDuringTransfer | выполнено на тестовом шифре |
| FR-013 приём на другом устройстве не забирается обратно сам | delivery/TestStreamEndings/STREAM_REPLACED, e2e/TestReceiveElsewhere (15 с без повторного открытия, уведомление, ELSEWHERE), UI/ChatsTab.test («Получать здесь») | выполнено на тестовом шифре |
| FR-014 временные отказы — повтор без потери подключения | delivery/TestSendErrors (CHAT_NOT_READY, SERVER_BUSY, NOT_CONNECTED, потерянный ответ), e2e/TestServerRestartDuringTransfer | выполнено на тестовом шифре |
| FR-015 заполненная очередь собеседника | delivery/TestQueueFull (WAITING/QUEUE_FULL, паузы 3, 6, 12, 15 с, одно уведомление, продолжение с первой неотправленной части), e2e/TestQueueFullOfflineReceiver (70 МиБ при закрытом получателе) | выполнено на тестовом шифре |
| FR-016 дубликат после потерянного ответа не искажает сообщение | delivery/TestSendErrors/потерянный ответ | выполнено на тестовом шифре |
| FR-017 продолжение после перезапуска без повторной отправки | delivery/TestSenderRestart (каждая часть отправлена ровно один раз), delivery/TestSendErrors/UNAUTHENTICATED, e2e/TestClientRestarts | выполнено на тестовом шифре |
| FR-018 повреждённые и чужие части | delivery/TestBadFragments (мусор, чужой чат, Total 5 463, несовпадение Total), delivery/TestTamperedCiphertext (PKCS7 → CORRUPTED/MESSAGE_CORRUPTED) | выполнено на тестовом шифре |
| FR-019 показ текста, картинок и файлов | UI/MessageItem.test (текст как текст, `<img>` по mediaUrl, файл с именем и размером, «Сохранить как…»), delivery/TestImagesAndNames, сборка окна: запрос `/media/0` из окна дошёл до обработчика (`"media":"handler"` в пробном запуске обеих сборок) | выполнено на тестовом шифре; показ настоящей картинки в окне — проверяется вручную |
| FR-020 картинка по содержимому | delivery/payload_test (PNG, JPEG, GIF, WebP, BMP; SVG и HTML с типом image — файл), delivery/TestImagesAndNames, delivery/TestLargeImageIsFile (больше 20 МиБ — файл), delivery/TestMediaHandler | выполнено на тестовом шифре |
| FR-021 имя файла только как предложение в диалоге | delivery/payload_test TestSafeFileName (`..\..\evil.exe`, `CON`, недопустимые символы), delivery/TestImagesAndNames | выполнено на тестовом шифре; стандартный диалог сохранения — проверяется вручную |
| FR-022 сообщения и файлы в базе профиля | localdb/messages_test, service/TestHistoryAndUnread, e2e/TestHistoryAfterRestart | выполнено на тестовом шифре |
| FR-023 история закрытого чата только для чтения, удаление с подтверждением | service/TestClosedChatHistory, e2e/TestHistoryAfterRestart, UI/ClosedChats.test, UI/ConversationView.test (лента только для чтения) | выполнено на тестовом шифре |
| FR-024 число новых сообщений, открытый чат сразу просмотрен | service/TestHistoryAndUnread, UI/ChatsTab.test (значок), UI/ConversationView.test (MarkRead при новом входящем) | выполнено на тестовом шифре |
| FR-025 полосы прогресса четырёх стадий | delivery/TestProgressStages (первое событие не позже 1 с, промежутки от 250 мс до 1 с, последнее done = total), delivery/progress_test, UI/ProgressBars.test | выполнено на тестовом шифре |
| FR-026 отмена каждой операции | delivery/TestCancelOutgoing, TestCancelReceiving, TestCancelDecrypt; e2e/TestCancelOperations | выполнено на тестовом шифре |
| FR-027 получатель узнаёт об отмене начатой отправки | delivery/TestCancelOutgoing, delivery/TestCancelBeforeParts, delivery/TestSourceChanged, e2e/TestCancelOperations (CANCELLED_BY_PEER) | выполнено на тестовом шифре |
| FR-028 отмена приёма удаляет части, поздние отбрасываются | delivery/TestCancelReceiving (поток не закрыт, следующее сообщение принято) | выполнено на тестовом шифре |
| FR-029 отключение и выход не подтверждают несохранённое | delivery/TestDisconnectAndParallelCancel, e2e/TestCancelOperations (отключение и подключение посреди приёма — файл целый) | выполнено на тестовом шифре |
| FR-030 повтор расшифрования после отмены | delivery/TestCancelDecrypt, e2e/TestCancelOperations (RetryDecrypt — файл равен исходному) | выполнено на тестовом шифре |
| FR-031 ключ и открытые данные не на сервере, не в журналах, не в интерфейсе | client/TestMessageDTOsHaveNoSecrets (JSON видов и событий без ключа, IV, шифротекста и message_id), e2e/TestTextMessaging (метки текстов и сеансовые ключи, 228 строк поиска, не найдены в журналах сервера, pg_dump и файлах брокера), 007-imports (во фронтенде нет криптографии) | выполнено на тестовом шифре; независимая проверка — за координатором |
| FR-032 ошибки не завершают приложение | delivery/TestStoreFailureAndPanic (паника обработки → уведомление INTERNAL, приём продолжается), все сценарии сбоев в delivery/faults_test и e2e без аварийного завершения процесса | выполнено на тестовом шифре |
| FR-033 окно отзывчиво при больших файлах | e2e/TestMemory256 (рост памяти, см. SC-008) | память — выполнено на тестовом шифре; отзывчивость окна — проверяется вручную |
| FR-034 сдаваемая сборка не отправляет | delivery/TestReleaseBuild (FAILED/ALGORITHM_UNAVAILABLE, 0 вызовов SendFragment, принятые части в DECRYPT_PENDING), service/TestTextValidation без тега; 007-imports: `stubcipher` только с тегом; пробный запуск сдаваемой сборки — алгоритмы недоступны | выполнено |
| FR-035 таблицы 006 не меняются, сервер не меняется | localdb/TestMigrationOver006, 007-imports (`git diff` по core, server, proto, integration пуст; новых зависимостей нет) | выполнено |

## Критерии успеха

| критерий | способ | результат |
|---|---|---|
| SC-001 текст и файл во всех 28 сочетаниях при всех длинах ключа | e2e/TestTextMessaging (тексты, 28 сочетаний, в обе стороны); delivery/TestTextAllModes (тексты 7 длин × 28 сочетаний); delivery/TestFiles (файлы 0 Б, 1 Б, 49 153 Б × 28 сочетаний, после полного прогона расширен и перезапущен с `-run`, отмечено в отчёте); e2e/TestFileMessaging (файлы при CBC/Zeros, CBC/PKCS7, CTR/ISO 10126) | выполнено на тестовом шифре; файлы во всех 28 сочетаниях проверены на подделке сервера, на настоящем сервере — в трёх сочетаниях |
| SC-002 текст не позже 5 с | замер e2e: худший из 5 — 107 мс | выполнено на тестовом шифре |
| SC-003 файл 50 МиБ не дольше 3 мин | замер e2e (CTR, ISO 10126): 57 с | выполнено на тестовом шифре |
| SC-004 сбои дают одно целое сообщение | delivery/faults_test, e2e/TestClientRestarts, TestServerRestartDuringTransfer, TestQueueFullOfflineReceiver | выполнено на тестовом шифре |
| SC-005 возобновление не позже 10 с после возврата сервера | замер e2e: 404 мс до первой новой принятой части | выполнено на тестовом шифре |
| SC-006 прогресс не позже 1 с и не реже раза в секунду, отмена не дольше 2 с | delivery/TestProgressStages; замеры e2e: отмена передачи 165 мс, приёма 5 мс, расшифрования 3 мс | выполнено на тестовом шифре |
| SC-007 нет открытых данных и ключей на сервере и в брокере | e2e: поиск меток текстов, содержимого, имён файлов и сеансовых ключей в журналах сервера, pg_dump и файлах `/var/lib/rabbitmq` — 0 совпадений (в том числе пока части лежали в очереди при заполнении) | выполнено на тестовом шифре |
| SC-008 рост памяти не больше 64 МиБ при файле 256 МиБ, окно отвечает | замер e2e: прирост HeapInuse процесса теста с обоими сервисами — 20,0 МиБ (порог теста 128 МиБ на два сервиса); передача 256 МиБ заняла 4 мин 55 с | выполнено, замер процесса теста (на тестовом шифре, а не окна); отзывчивость окна — проверяется вручную |
| SC-009 история после перезапуска совпадает | e2e/TestHistoryAfterRestart, service/TestHistoryAndUnread | выполнено на тестовом шифре |
| SC-010 нет аварийных завершений | все модульные и сквозные сценарии сбоев, отмены и повреждённых данных прошли в одном процессе теста | выполнено на тестовом шифре |
| SC-011 сдаваемая сборка не отправляет | delivery/TestReleaseBuild, service/TestTextValidation без тега, 007-imports, пробный запуск сдаваемой сборки | выполнено |

## Тесты исправлений по независимым проверкам

После независимых проверок кода (`007-crypto-check.md`, `007-design-review.md`) найденные ошибки исправлены, на каждое исправление добавлен тест, который не проходил до исправления. Результаты — в `007-test-full.txt`, раздел «повтор после исправлений по проверкам».

| требование | тест | что проверяет |
|---|---|---|
| FR-004, FR-026 | delivery/TestAssembleAllFragmentsFail, delivery/TestAssembleCancelledMultipart, delivery/TestCancelMultipartDecryptStops | все части пачки ошибочны или расшифрование отменено — сборка завершается, место расшифрования освобождается, остановка доставки не зависает |
| FR-018, FR-027 | delivery/TestClassify (отмена 15, 17 байт и 1 МиБ) | служебной отменой считается только содержимое ровно 16 байт |
| FR-027 | delivery/TestCancelAfterUncertainSend | отмена после отправки части с неизвестным исходом (потерянный ответ, отмена во время вызова) доходит до получателя |
| FR-030 | delivery/TestRetryRightAfterCancel | повтор расшифрования сразу после отмены не теряется |
| FR-033 | delivery/TestEncryptAheadLimit | при медленной отправке шифрование опережает её не больше чем на 16 частей |
| FR-022, FR-031 | localdb/TestWipedChunksLeaveNoBytes | после отмены, сбоя, повреждения и возврата расшифрования байтов открытого куска нет ни в базе, ни в журнале WAL |
| FR-018 | delivery/TestParamsErrorKeepsParts | ошибка параметров чата оставляет сообщение в ожидании с причиной, части не удаляются |
| FR-032 | delivery/TestEncryptWriteFailureNotice | одно уведомление LOCAL_WRITE на сообщение, паузы 1, 2, 4, 5 с |
| FR-021 | delivery/TestSafeFileName | символы категории Cf (смена направления письма и другие) заменяются на `_` |
| FR-013 | delivery/TestReceiveHereRightAfterElsewhere | «Получать здесь» сразу после перехода в ELSEWHERE открывает приём |
| FR-023, FR-027 | localdb/TestFinishAndHistory, delivery/TestCancelBeforeParts | строка-признак отмены, пришедшей раньше частей, не видна в истории и не считается в закрытых чатах |
| FR-002 | delivery/TestTextMagentaSubstitute | текст в чате MAGENTA (тестовая замена) в CBC/PKCS7 и CTR/Zeros |
| FR-019 | UI/MessageItem.test | «получение…» только в RECEIVING, иначе состояние словами, для CORRUPTED и KEY_MISSING — «сообщение недоступно»; уведомление «Сохранено: <путь>» |
| FR-019, FR-024 | UI/ConversationView.test | прокрутка к концу при открытии и новом сообщении, если пользователь был внизу; подгрузка ранних сохраняет место |
| FR-006, FR-013 | UI/Composer.test | в подсказке о блокировке — «Подключиться» и «Получать здесь» |
| FR-025 | UI/messages.test | прогресс стадии, не подходящей состоянию, не показывается |
| FR-001, FR-021 | client/TestDialogErrors, UI/Composer.test, UI/MessageItem.test | сбой диалога выбора или сохранения показывается кодом ошибки, а не как закрытый диалог |

## Проверяется вручную (quickstart, раздел 7)

- стандартные диалоги выбора и сохранения файла (пункт 3);
- показ настоящей картинки в ленте окна (пункт 3; доставка запроса окна до обработчика `/media/` проверена пробным запуском);
- отзывчивость окна при передаче 200–256 МиБ и отмена на середине каждой стадии в окне (пункт 4);
- надпись «Алгоритм чата не реализован в этой сборке» в окне сдаваемой сборки (пункт 8; в тестах интерфейса и сервиса проверена);
- удобство интерфейса.

## Не проверено

- сдаваемая сборка с настоящими MAGENTA и RC6: шифры не реализованы, все сценарии переписки выполнены с тестовым шифром из тега `devcipher`;
- независимые проверки криптографии и проекта (выполняет координатор).
