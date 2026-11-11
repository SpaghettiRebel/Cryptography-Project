-- Переписка: сообщения, открытое содержимое файлов, шифротекст к отправке и принятый шифротекст.
-- Таблицы версии 1 не меняются.

-- direction: 1 исходящее, 2 входящее. kind: 0 неизвестно, 1 текст, 2 файл, 3 картинка, 4 служебное.
CREATE TABLE messages (
    seq             INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id         BLOB NOT NULL REFERENCES chats (chat_id) ON DELETE CASCADE,
    message_id      BLOB NOT NULL CHECK (length(message_id) = 16),
    direction       INTEGER NOT NULL CHECK (direction IN (1, 2)),
    kind            INTEGER NOT NULL CHECK (kind BETWEEN 0 AND 4),
    state           INTEGER NOT NULL,
    error_code      TEXT NULL,
    created_at      INTEGER NOT NULL,
    completed_at    INTEGER NULL,
    read_at         INTEGER NULL,
    text            TEXT NULL,
    file_name       TEXT NULL,
    file_type       TEXT NULL,
    media_type      TEXT NULL,
    size            INTEGER NULL,
    total           INTEGER NULL,
    encrypted_count INTEGER NOT NULL DEFAULT 0,
    sent_count      INTEGER NOT NULL DEFAULT 0,
    received_count  INTEGER NOT NULL DEFAULT 0,
    source_path     TEXT NULL,
    source_size     INTEGER NULL,
    source_mtime    INTEGER NULL,
    chunks_complete INTEGER NOT NULL DEFAULT 0,
    UNIQUE (chat_id, direction, message_id)
);

CREATE INDEX messages_chat_seq ON messages (chat_id, seq);
CREATE INDEX messages_state ON messages (state);

-- Открытое содержимое файлов кусками по 49 152 байта; кусок idx лежит по смещению idx * 49152.
CREATE TABLE message_chunks (
    seq  INTEGER NOT NULL REFERENCES messages (seq) ON DELETE CASCADE,
    idx  INTEGER NOT NULL,
    data BLOB NOT NULL CHECK (length(data) <= 49152),
    PRIMARY KEY (seq, idx)
);

-- Сериализованные фрагменты к отправке; строка удаляется вместе с ростом sent_count.
CREATE TABLE out_fragments (
    seq  INTEGER NOT NULL REFERENCES messages (seq) ON DELETE CASCADE,
    idx  INTEGER NOT NULL,
    wire BLOB NOT NULL CHECK (length(wire) <= 61440),
    PRIMARY KEY (seq, idx)
);

-- Принятые фрагменты до расшифрования, в том виде, как пришли.
CREATE TABLE in_fragments (
    seq  INTEGER NOT NULL REFERENCES messages (seq) ON DELETE CASCADE,
    idx  INTEGER NOT NULL,
    wire BLOB NOT NULL,
    PRIMARY KEY (seq, idx)
);

-- Желание держать поток приёма чата открытым. Нет строки — как подключение на сервере.
CREATE TABLE chat_receive (
    chat_id    BLOB PRIMARY KEY REFERENCES chats (chat_id) ON DELETE CASCADE,
    wanted     INTEGER NOT NULL CHECK (wanted IN (0, 1)),
    updated_at INTEGER NOT NULL
);
