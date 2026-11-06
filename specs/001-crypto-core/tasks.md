# Tasks: Криптоядро курсового проекта

**Input**: Design documents from `/specs/001-crypto-core/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/

**Tests**: обязательны по принципу V конституции и критериям SC-001…SC-012. Тесты каждой истории пишутся перед реализацией и сначала падают.

**Organization**: задачи сгруппированы по историям из spec.md: US1 и US2 (P1), затем US3 (P2).

## Format: `[ID] [P?] [Story] Description`

- **[P]**: можно выполнять параллельно: разные файлы и нет зависимости от незавершённых задач
- **[Story]**: история из spec.md
- Пути указаны относительно папки `project`; команды Go выполняются из `project/core`

## Общие правила

- Python и готовые шифры, режимы и обмен ключами запрещены: `crypto/aes`, `crypto/des`, `crypto/rc4`, `crypto/cipher`, `crypto/ecdh`, `crypto/mlkem`, шифры из `golang.org/x/crypto`. `big.Int.Exp` с модулем не используется. Разрешены `crypto/rand`, `crypto/hmac`, `crypto/sha256`, `math/big`.
- Для детерминированных тестов используется внутренний источник в самом тесте, а не `math/rand`.
- Ошибки — сентинелы, проверяются через `errors.Is`. `panic` только при нарушении инварианта программиста. Ключи, секреты и данные не попадают в ошибки и логи.
- Комментарии в коде на русском, как в лабораторных.
- Тесты длиннее ~1 с пропускаются под `testing.Short()`. Перед отметкой задачи выполненной `go build ./...`, `go vet ./...` и тесты затронутого пакета должны быть зелёными.
- Придуманные тестовые векторы недопустимы. Значения из RFC сверяются с текстом RFC.
- Импорт из `lab1`–`lab3` запрещён, код переносится копированием. Шифров MAGENTA и RC6 в этой фиче нет: они в `specs/002-ciphers`.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: модуль и рабочая область

- [X] T001 Создать `go.work` (`go 1.22`, `use ./core`) и `core/go.mod` (`module cryptochat/core`, `go 1.22`), каталоги `core/cipher`, `core/algorithms`, `core/internal/stubcipher`, `core/modes`, `core/padding`, `core/cryptoctx`, `core/dh`, `core/kdf`, `core/protocol`, `core/message`, `report/runs`

**Checkpoint**: `go build ./...` в `core` выполняется без ошибок.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: интерфейс и тестовый шифр нужны режимам и сообщениям. US2 (согласование ключа) от `cipher.Algorithm` зависит только через `kdf`.

**⚠️ CRITICAL**: US1 и US3 не начинаются, пока фаза не завершена.

- [X] T002 Перенести `../lab3/internal/modes/modes.go` и `modes_test.go` в `core/modes/`, заменив путь модуля на `cryptochat/core`
- [X] T003 Реализовать в `core/cipher/cipher.go` интерфейс `BlockCipher` (`SetKey`, `EncryptBlock`, `DecryptBlock`, `BlockSize`), тип `Algorithm uint8` с `MAGENTA = 1`, `RC6 = 2`, тип `Settings` (`Algorithm`, `KeyBits`, `Mode`, `Padding`) с перечислениями из `core/modes` и сентинелы `ErrAlgorithmUnavailable`, `ErrInvalidKeySize`, `ErrInvalidBlockSize`, `ErrKeyNotSet`. Описать в комментарии контракт: после `SetKey` безопасен при одновременной обработке блоков, не меняет входные срезы, не хранит ссылок
- [X] T004 [P] Перенести `../lab3/internal/padding/padding.go` и `padding_test.go` в `core/padding/`, заменив путь модуля; при любом нарушении `Unpad` возвращает ровно `ErrBadPadding`
- [X] T005 [P] Написать `core/internal/stubcipher/stub_test.go`: блок 16 байт, ключи 16, 24, 32 байта; `Decrypt(Encrypt(x)) = x` на случайных блоках; разные ключи дают разный результат; ошибки длины ключа и блока, отсутствие ключа; параллельное использование под `-race`; входные срезы не меняются
- [X] T006 Реализовать в `core/internal/stubcipher/stub.go` тестовый шифр: обратимая сеть Фейстеля на сложении, циклическом сдвиге и XOR, реализующая `cipher.BlockCipher`, с настраиваемой искусственной нагрузкой на блок для SC-007; комментарий обязан сказать, что шифр не стойкий и нужен только для тестов и разработки

**Checkpoint**: `go test ./modes ./padding ./internal/stubcipher` зелёные. Пакет `padding` импортирует `modes`, поэтому T002 идёт первым.

---

## Phase 3: User Story 1 - Шифрование данных в режимах с набивкой через общий интерфейс (Priority: P1) 🎯 MVP

**Goal**: реестр алгоритмов и 7 режимов с 4 набивками поверх любого `BlockCipher`.

**Independent Test**: `go test ./algorithms ./cryptoctx -v` проходит на тестовом шифре без настоящих алгоритмов.

### Tests for User Story 1

- [X] T007 [P] [US1] `TestUnavailable` и `TestNoStubInReleaseBuild` в `core/algorithms/registry_test.go`: `MAGENTA`, `RC6` и идентификатор `255` дают `ErrAlgorithmUnavailable`; `Available()` пуст; `go list` по шаблону `cryptochat/core/...` без тега: `internal/stubcipher` нет в зависимостях других пакетов. Шаблон указывает весь модуль, потому что тест запускается из папки пакета, а `./...` охватил бы только её; сам `stubcipher` входит в шаблон и исключается; файл под `//go:build !devcipher` (SC-012)
- [X] T008 [P] [US1] `TestDevCipherAvailable` в `core/algorithms/registry_dev_test.go` под `//go:build devcipher`: идентификатор `255` создаёт рабочий шифр и входит в `Available()`; недопустимый `keyBits` даёт `ErrInvalidKeySize`
- [X] T009 [P] [US1] Перенести из `../lab3/internal/cipherctx` в `core/cryptoctx/`, заменив Rijndael на `stubcipher` в общем помощнике `helpers_test.go`: `cipherctx_test.go`, `counter_test.go`, `async_test.go`, `edge_test.go` и из `modes_test.go` тесты `TestPCBC_Structure`, `TestModes_ParallelMatchesSequential`, `TestModes_ErrorPropagation`, `TestContext_ConcurrentUse`. Тесты `TestModesSP80038A` и `samples_test.go` не переносить: режимы уже сверены со стандартом в `lab3`, а там нужен AES
- [X] T010 [P] [US1] `TestModesByDefinition` в `core/cryptoctx/definition_test.go`: для ECB, CBC, PCBC, CFB, OFB, CTR, Random Delta шифротекст совпадает с вычислением в тесте по определению режима, отдельным кодом поверх `stubcipher.EncryptBlock`, без пакетов `cryptoctx` и `padding`, минимум на 4 блоках данных, для расшифрования — обратно. Random Delta считается по определению из `specs/001-crypto-core/research.md`, раздел R10 (SC-002)
- [X] T011 [US1] `TestMatrix` в `core/cryptoctx/matrix_test.go`: 3 длины ключа × 7 режимов × 4 набивки × длины 0, 1, 15, 16, 17 байт, данные без нулей в конце; восстановление побайтово; разные IV дают разный шифротекст в режимах с IV (SC-001)
- [X] T012 [US1] `TestNewWithCipher` в `core/cryptoctx/keyed_test.go`: контексты на одном шифре с заданным ключом работают параллельно под `-race`, `SetKey` при этом не вызывается (счётчик в обёртке теста), результат совпадает с обычным `New`

### Implementation for User Story 1

- [X] T013 [US1] Реализовать в `core/algorithms/registry.go` таблицу реализаций, `New(a cipher.Algorithm, keyBits int) (cipher.BlockCipher, error)` и `Available()`; сейчас таблица пуста, `002-ciphers` добавит в неё записи
- [X] T014 [US1] Реализовать в `core/algorithms/registry_dev.go` под `//go:build devcipher` регистрацию `stubcipher` с идентификатором `255`
- [X] T015 [US1] Перенести `cipherctx.go`, `modes_impl.go`, `options.go`, `parallel.go` из `../lab3/internal/cipherctx/` в `core/cryptoctx/`, переименовав пакет в `cryptoctx`, заменив путь модуля и `ciphercore` на `core/cipher`
- [X] T016 [US1] Добавить в `core/cryptoctx/cipherctx.go` `NewWithCipher(keyed cipher.BlockCipher, mode, pad, opts...)`: те же проверки, что в `New`, но без `SetKey`

**Checkpoint**: режимы и набивки работают и проверены независимо от остальных пакетов.

---

## Phase 4: User Story 2 - Согласование сеансового ключа (Priority: P1)

**Goal**: Диффи-Хеллман и вывод сеансового ключа точной длины.

**Independent Test**: `go test ./dh ./kdf -v` проходит.

**Зависит от**: T001; для `kdf` — T003. От US1 не зависит.

### Tests for User Story 2

- [X] T017 [P] [US2] `TestGroup14Formula` в `core/dh/group_test.go`: `P` совпадает с пересчётом по формуле RFC 3526 `2²⁰⁴⁸ − 2¹⁹⁸⁴ − 1 + 2⁶⁴·(⌊2¹⁹¹⁸π⌋ + 124476)`, π считается в тесте по формуле Мэчина; `G = 2`; `P` начинается с `ffffffffffffffffc90fdaa22168c234`; `Group14()` возвращает независимую копию
- [X] T018 [P] [US2] `TestModPow` в `core/dh/modpow_test.go`: сверка с медленным циклом умножений на малых входах; показатель 0, модуль 1, основание больше модуля; ошибки для неположительного модуля и отрицательного показателя
- [X] T019 [P] [US2] `TestAgreement`, `TestValidatePublic`, `TestSecretNotPrinted` в `core/dh/dh_test.go`: 1000 согласований под `!testing.Short()` (10 в быстром наборе), общий секрет ровно 256 байт и одинаков; у согласования секретное значение новое; «значения вне диапазона [2, p − 2]» дают `ErrInvalidPublicValue`: 0, 1, p − 1, p, p + 1, отрицательное, nil; `fmt` с `%v`, `%+v`, `%#v` и `%s` не выводит секретное значение; время одной стороны не больше 1 с; при отказе источника случайности, поданного через неэкспортируемую функцию `generateKey(r io.Reader)`, ошибка возвращается вызывающему коду (SC-003, SC-010)
- [X] T020 [P] [US2] `TestHKDFRFC5869` и `TestSessionKey` в `core/kdf/kdf_test.go`: тест A.1 из RFC 5869, `OKM = 3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865` для `IKM = 0x0b × 22`, `salt = 000102…0c`, `info = f0f1…f9`, `L = 42`; сверить значения с текстом RFC. `SessionKey`: длина `keyBits / 8`; другие чат, алгоритм или длина дают другой ключ; один вход детерминированно даёт один результат; неверная длина секрета и `keyBits` дают ошибку

### Implementation for User Story 2

- [X] T021 [US2] Реализовать в `core/dh/modpow.go` `ModPow`, перенеся алгоритм из `../lab2/internal/numtheory/modpow.go`, с собственными сентинелами ошибок
- [X] T022 [US2] Реализовать в `core/dh/group.go` `Group` и `Group14()` с шестнадцатеричной константой группы 14 RFC 3526 и `G = 2`; значение сверить с текстом RFC; метод `ValidatePublic`: допустимо только `2 ≤ y ≤ p − 2`, сентинел `ErrInvalidPublicValue`
- [X] T023 [US2] Реализовать в `core/dh/keypair.go` `GenerateKey` (секретное значение равномерно из [2, p − 2]; публичный метод всегда вызывает неэкспортируемую `generateKey(r io.Reader)` с `crypto/rand.Reader`), `Public`, `SharedSecret` (проверяет значение собеседника, результат ровно 256 байт, big-endian, с ведущими нулями) и методы форматирования, скрывающие секрет
- [X] T024 [US2] Реализовать в `core/kdf/kdf.go` `HKDF(ikm, salt, info, length)` по RFC 5869 на `crypto/hmac` и `crypto/sha256` и `SessionKey(shared, chatID, algorithm, keyBits)`: `salt = chatID`, `info = "cryptochat/session-key/v1" ‖ Algorithm ‖ KeyBits (2 байта)`, вход ровно 256 байт

**Checkpoint**: две стороны получают один ключ, значения RFC совпадают.

---

## Phase 5: User Story 3 - Фрагментация сообщений (Priority: P2)

**Goal**: двоичный формат фрагмента и потоковая обработка с параллельностью, прогрессом и отменой.

**Independent Test**: `go test ./protocol ./message -v` проходит: сообщение восстанавливается из перемешанных фрагментов, а в открытых полях нет данных.

**Зависит от**: Phase 2 и US1.

### Tests for User Story 3

- [X] T025 [P] [US3] `TestRoundTrip`, `TestBoundaries`, `TestCorruptedInputs` и `FuzzUnmarshal` в `core/protocol/fragment_test.go`: сериализация обратима; отвергаются неверные Magic и Version, «`Index < Total`», «`1 ≤ Total ≤ 2²⁴`», IVLen кроме 0 и 16, «`1 ≤ CipherLen ≤ 1 МиБ + 4 КиБ`», усечение, лишние байты; заявленный размер больше данных отвергается без выделения памяти; не менее 100 000 случайно повреждённых вариантов без паники (SC-005)
- [X] T026 [P] [US3] `TestNoCipherDependency` в `core/protocol/deps_test.go`: `go list -deps` пакета `protocol` не содержит `core/cipher`, `core/algorithms`, `core/cryptoctx`, `core/modes`, `core/padding`, `core/message` (FR-021)
- [X] T027 [P] [US3] `TestRoundTrip` в `core/message/roundtrip_test.go`: размеры 0, 1 байт, 1 МиБ, 1 МиБ + 1 и данные с нулями в конце при всех режимах и набивках; 100 МБ при всех 7 режимах под `!testing.Short()`; восстанавливаются данные, имя, тип; порядок фрагментов перемешан (SC-004). Использовать шифр через внутреннюю точку подмены, из `stubcipher`
- [X] T028 [P] [US3] `TestAssemblerErrors` в `core/message/assembler_test.go`: пропуск → `ErrMissingFragments`, после дозагрузки `Finish` успешен; точный повтор игнорируется; чужие чат и сообщение, несовпадение внутренней привязки с открытыми полями, различие `Total` или `TotalSize`, неверная сумма `DataLen`, длина куска не 1 МиБ не в последнем фрагменте → `ErrFragmentMismatch`; неверный ключ → ошибка без паники; одновременные `Add` под `-race`; `Finish` не возвращает `Meta` до получения фрагмента 0; `Total`, не равный `max(1, ⌈TotalSize / 1 МиБ⌉)`, → `ErrFragmentMismatch`; `Seal` возвращает ошибку, если читатель короче или длиннее `TotalSize`, если `emit` вернул ошибку, если `TotalSize` отрицателен или требует больше 2²⁴ фрагментов и если отказал источник случайности для IV или `Fill`; имя длиннее 1024 байт и тип длиннее 255 байт отвергаются
- [X] T029 [P] [US3] `TestOpenFieldsLeak` в `core/message/leak_test.go`: в сериализованных фрагментах целиком нет байтов имени, типа и контрольных данных при всех режимах; в поточных режимах длина шифротекста кратна блоку и не позволяет отличить размеры данных внутри одного блока; у внутренней структуры `Fill ≥ 1` байта и последний байт ненулевой; разные фрагменты одного сообщения имеют разные IV (SC-011, research R7)
- [X] T030 [P] [US3] `TestParallel` в `core/message/parallel_test.go`: при детерминированном источнике случайности через внутреннюю точку подмены 1 и 8 рабочих дают побайтово одинаковые фрагменты; `emit` вызывается строго по порядку; на 4 и более ядрах и `stubcipher` с нагрузкой ускорение не меньше 2 раз, под `!testing.Short()`, при меньшем числе ядер тест пропускается с сообщением (SC-006, SC-007)
- [X] T031 [P] [US3] `TestMemory`, `TestCancel`, `TestProgress` в `core/message/resource_test.go`: 100 МБ из потокового читателя и запись в отбрасывающий `io.WriterAt`, счётчик живых буферов на точке подмены не больше 16 фрагментов, а замер кучи при `debug.SetGCPercent(10)` печатается и проверяется с указанным в комментарии запасом на накладные расходы среды; отмена до начала, в середине и сразу после завершения, возврат ошибки `context` не дольше 1 с на медленном шифре; прогресс не убывает, вызовы не пересекаются, последний равен полному размеру, в том числе для пустого сообщения (SC-008, SC-009)
- [X] T032 [P] [US3] `TestPublicAPI` в `core/message/public_dev_test.go` под `//go:build devcipher`: полный цикл через `algorithms.New` с идентификатором `255`, без внутренней точки подмены

### Implementation for User Story 3

- [X] T033 [US3] Реализовать в `core/protocol/fragment.go` `Fragment`, `Marshal`, `Unmarshal`, сентинелы `ErrMalformedFragment`, `ErrUnsupportedVersion`, константы размеров. Формат big-endian: Magic `"CF"`, Version `1`, ChatID и MessageID по 16 байт, Index и Total по 4 байта, IVLen 1 байт, IV, CipherLen 4 байта, Cipher; размеры проверяются до выделения памяти; без импорта шифров
- [X] T034 [US3] Реализовать в `core/message/inner.go` внутреннюю структуру: Version, ChatID, MessageID, Index, TotalSize, NameLen, TypeLen, Name, Type, DataLen, Data, Fill; «имя до 1024 байт, тип до 255»; имя и тип только во фрагменте 0; `Fill` — случайные байты до границы блока, «минимум 1 байт», последний ненулевой; разбор возвращает `ErrFragmentMismatch` или `ErrBadPadding` без паники
- [X] T035 [US3] Реализовать в `core/message/seal.go` `Seal`: чтение кусков по 1 МиБ, новый IV из `crypto/rand` на фрагмент, если режим его использует, `cryptoctx.NewWithCipher` с `WithWorkers(1)`, окно из `W = min(GOMAXPROCS, 5)` фрагментов, выдача `emit` строго по порядку из одной горутины, прогресс, отмена, `Total = max(1, ⌈TotalSize / 1 МиБ⌉)`, поэтому ровно один фрагмент для пустого сообщения; проверка `TotalSize`, включая предел 2²⁴ фрагментов. Точки подмены шифра, числа рабочих и источника случайности — только неэкспортируемая структура `sealConfig`, передаваемая во внутреннюю функцию `seal`; глобальных изменяемых переменных нет, а публичный `Seal` всегда использует `algorithms.New`, `crypto/rand.Reader` и `W`
- [X] T036 [US3] Реализовать в `core/message/assembler.go` `NewAssembler`, `Add`, `Finish`: запись в `io.WriterAt` по смещению `index · 1 МиБ`, битовая карта номеров, проверки согласованности, повтор игнорируется, безопасность при одновременных `Add`, прогресс; `Total` не больше 2²⁴ и равен `max(1, ⌈TotalSize / 1 МиБ⌉)`; подмена шифра — только через неэкспортируемую функцию `newAssembler`

**Checkpoint**: сообщения любого размера восстанавливаются, память ограничена, в открытых полях нет данных.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: проверки перед завершением фичи

- [X] T037 Прогнать агента `test-runner` и все команды из `specs/001-crypto-core/quickstart.md`, включая `go test -short -race ./...`, `go test -tags devcipher ./...` и `go test ./protocol -fuzz FuzzUnmarshal -fuzztime 30s`; сохранить вывод в `report/runs/001-test-full.txt`. Для `-race` в Git Bash `/c/msys64/ucrt64/bin` должен стоять первым в PATH
- [X] T038 Проверить запреты командой `go list -f "{{.ImportPath}}: {{join .Imports \" \"}}" ./...`: нет прямых импортов запрещённых пакетов и нет импорта лабораторных работ; записать результат в `report/runs/001-imports.txt`
- [X] T039 [P] Прогнать агента `crypto-checker` по `core`, исправить расхождения и записать результат в `report/runs/001-crypto-check.md`; проверка MAGENTA и RC6 отмечается как относящаяся к `002-ciphers`
- [X] T040 [P] Прогнать агента `design-reviewer` по `core`, исправить замечания и записать результат в `report/runs/001-design-review.md`
- [X] T041 Написать раздел об ограничениях в `report/note.md` агентом `report-writer` по research R9 и фактическим прогонам: нет аутентификации, подмена при Диффи-Хеллмане, нет работы за постоянное время, Zeros и ECB, открытый размер с точностью до 1 МиБ; указать источники RFC (FR-024)
- [X] T042 Проверить каждый критерий SC-001…SC-012 и требование FR-001…FR-024 по фактическим результатам, записать таблицу в `report/runs/001-acceptance.md`; отметить перенесённое в `002-ciphers`

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: без зависимостей
- **Foundational (Phase 2)**: после Setup
- **US1 (Phase 3)**: после Phase 2
- **US2 (Phase 4)**: после Setup; пакету `kdf` нужен T003; может идти параллельно с US1
- **US3 (Phase 5)**: после Phase 2 и US1
- **Polish (Phase 6)**: после всех историй

### User Story Dependencies

```text
Setup → Foundational → US1 → US3 ─┐
   └───────────────► US2 ─────────┴─► Polish
```

### Within Each User Story

- Тесты пишутся первыми и сначала падают
- Порядок реализации US3: `protocol` → `inner` → `seal` → `assembler`
- История считается готовой после своего checkpoint

### Parallel Opportunities

- T004 и T005 — в разных пакетах, после T003
- Тесты с [P] внутри истории — в разных файлах. T011 и T012 используют `helpers_test.go` из T009, поэтому идут после него
- US1 и US2 после Phase 2
- T039 и T040

---

## Parallel Example: User Story 3

```text
T025: core/protocol/fragment_test.go
T026: core/protocol/deps_test.go
T027: core/message/roundtrip_test.go
T028: core/message/assembler_test.go
T029: core/message/leak_test.go
T030: core/message/parallel_test.go
T031: core/message/resource_test.go
```

---

## Implementation Strategy

### MVP First

1. Setup и Foundational: интерфейс и тестовый шифр.
2. US1: режимы и набивки через общий интерфейс.
3. **Остановиться и проверить**: перебор и сверка с определениями режимов.

### Incremental Delivery

1. US1 → основа для шифров из `002-ciphers`
2. US2 → согласование ключа
3. US3 → формат фрагмента, который нужен серверу и доставке
4. Polish → приёмка и ограничения для записки

### Notes

- Реализацию запускать в сессии, открытой прямо в `project`, иначе хуки не работают.
- После этой фичи `002-ciphers` можно делать сразу по приходу документации.
