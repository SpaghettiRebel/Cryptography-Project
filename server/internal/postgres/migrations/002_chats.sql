CREATE TABLE chats (
    id           bytea PRIMARY KEY CHECK (octet_length(id) = 16),
    user_low     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    user_high    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    creator_id   uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    algorithm    smallint NOT NULL CHECK (algorithm BETWEEN 1 AND 2),
    key_bits     smallint NOT NULL CHECK (key_bits IN (128, 192, 256)),
    cipher_mode  smallint NOT NULL CHECK (cipher_mode BETWEEN 1 AND 7),
    padding_mode smallint NOT NULL CHECK (padding_mode BETWEEN 1 AND 4),
    created_at   timestamptz NOT NULL DEFAULT now(),
    closed_at    timestamptz,
    CHECK (user_low < user_high),
    CHECK (creator_id = user_low OR creator_id = user_high)
);

CREATE INDEX chats_low_active_idx ON chats (user_low) WHERE closed_at IS NULL;
CREATE INDEX chats_high_active_idx ON chats (user_high) WHERE closed_at IS NULL;
CREATE INDEX chats_pair_active_idx ON chats (user_low, user_high) WHERE closed_at IS NULL;

CREATE TABLE chat_participants (
    chat_id      bytea NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    connected    boolean NOT NULL,
    public_value bytea CHECK (public_value IS NULL OR octet_length(public_value) = 256),
    PRIMARY KEY (chat_id, user_id)
);

CREATE INDEX chat_participants_user_idx ON chat_participants (user_id);
