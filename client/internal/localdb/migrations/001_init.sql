-- Схема базы профиля клиента. Следующие версии добавляют таблицы, не меняя эти.
CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);

-- server_address, user_id, login
CREATE TABLE profile (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Локальная копия известных чатов. Строка остаётся после закрытия.
CREATE TABLE chats (
    chat_id       BLOB PRIMARY KEY CHECK (length(chat_id) = 16),
    peer_login    TEXT NOT NULL,
    creator_login TEXT NOT NULL,
    algorithm     INTEGER NOT NULL CHECK (algorithm IN (1, 2)),
    key_bits      INTEGER NOT NULL CHECK (key_bits IN (128, 192, 256)),
    cipher_mode   INTEGER NOT NULL CHECK (cipher_mode BETWEEN 1 AND 7),
    padding_mode  INTEGER NOT NULL CHECK (padding_mode BETWEEN 1 AND 4),
    created_at    INTEGER NOT NULL,
    closed_at     INTEGER NULL,
    synced_at     INTEGER NOT NULL
);

-- Секрет Диффи-Хеллмана записывается до публикации и удаляется вместе с записью ключа.
CREATE TABLE dh_secrets (
    chat_id       BLOB PRIMARY KEY REFERENCES chats (chat_id) ON DELETE CASCADE,
    private_value BLOB NOT NULL CHECK (length(private_value) = 256),
    public_value  BLOB NOT NULL CHECK (length(public_value) = 256),
    created_at    INTEGER NOT NULL,
    published_at  INTEGER NULL
);

-- Длина ключа key_bits / 8 проверяется в коде.
CREATE TABLE session_keys (
    chat_id     BLOB PRIMARY KEY REFERENCES chats (chat_id) ON DELETE CASCADE,
    session_key BLOB NOT NULL,
    derived_at  INTEGER NOT NULL
);
