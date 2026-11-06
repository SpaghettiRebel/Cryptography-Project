package postgres

import (
	"bytes"
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"cryptochat/server/internal/store"
)

// Чат читается с точки зрения участника $1. Ни один запрос не выдаёт значения, кроме PeerValue.
const chatSelect = `SELECT c.id, cu.id::text, cu.login, c.algorithm, c.key_bits, c.cipher_mode, c.padding_mode,
	c.created_at, c.closed_at,
	su.id::text, su.login, s.connected, s.public_value IS NOT NULL,
	pu.id::text, pu.login, p.connected, p.public_value IS NOT NULL
	FROM chats c
	JOIN chat_participants s ON s.chat_id = c.id AND s.user_id = $1::uuid
	JOIN users su ON su.id = s.user_id
	JOIN chat_participants p ON p.chat_id = c.id AND p.user_id <> $1::uuid
	JOIN users pu ON pu.id = p.user_id
	JOIN users cu ON cu.id = c.creator_id `

func scanChat(row pgx.Row) (store.Chat, error) {
	var c store.Chat
	var id []byte
	err := row.Scan(&id, &c.Creator.ID, &c.Creator.Login,
		&c.Params.Algorithm, &c.Params.KeyBits, &c.Params.CipherMode, &c.Params.PaddingMode,
		&c.CreatedAt, &c.ClosedAt,
		&c.Self.User.ID, &c.Self.User.Login, &c.Self.Connected, &c.Self.HasPublicValue,
		&c.Peer.User.ID, &c.Peer.User.Login, &c.Peer.Connected, &c.Peer.HasPublicValue)
	if err != nil {
		return store.Chat{}, err
	}
	copy(c.ID[:], id)
	return c, nil
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadChat(ctx context.Context, q querier, id [16]byte, userID string) (store.Chat, error) {
	return scanChat(q.QueryRow(ctx, chatSelect+`WHERE c.id = $2`, userID, id[:]))
}

// CreateChat создаёт чат и двух участников в одной транзакции. Контакт проверяется под блокировкой
// пары, поэтому чат не появится между бывшими контактами.
func (d *DB) CreateChat(parent context.Context, id [16]byte, creatorID, peerNorm string, p store.ChatParams) (store.Chat, error) {
	var out store.Chat
	err := d.inPairTx(parent, creatorID, peerNorm, store.ErrNotContact, func(ctx context.Context, tx pgx.Tx, peerID string) error {
		if peerID == creatorID {
			return store.ErrSelfChat
		}
		var isContact bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM contacts
			WHERE user_low = LEAST($1::uuid, $2::uuid) AND user_high = GREATEST($1::uuid, $2::uuid))`, creatorID, peerID).Scan(&isContact)
		if err != nil {
			return err
		}
		if !isContact {
			return store.ErrNotContact
		}
		_, err = tx.Exec(ctx, `INSERT INTO chats (id, user_low, user_high, creator_id, algorithm, key_bits, cipher_mode, padding_mode)
			VALUES ($1, LEAST($2::uuid, $3::uuid), GREATEST($2::uuid, $3::uuid), $2::uuid, $4, $5, $6, $7)`,
			id[:], creatorID, peerID, p.Algorithm, p.KeyBits, p.CipherMode, p.PaddingMode)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO chat_participants (chat_id, user_id, connected)
			VALUES ($1, $2::uuid, true), ($1, $3::uuid, false)`, id[:], creatorID, peerID)
		if err != nil {
			return err
		}
		out, err = loadChat(ctx, tx, id, creatorID)
		return err
	})
	if err != nil {
		return store.Chat{}, err
	}
	return out, nil
}

// ListActiveChats возвращает активные чаты, новые первыми.
func (d *DB) ListActiveChats(parent context.Context, userID string) ([]store.Chat, error) {
	ctx, cancel := withTimeout(parent)
	defer cancel()
	rows, err := d.pool.Query(ctx, chatSelect+`WHERE c.closed_at IS NULL ORDER BY c.created_at DESC, c.id`, userID)
	if err != nil {
		return nil, mapError(parent, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (store.Chat, error) { return scanChat(r) })
	if err != nil {
		return nil, mapError(parent, err)
	}
	return out, nil
}

// GetChat возвращает чат участника.
func (d *DB) GetChat(parent context.Context, id [16]byte, userID string) (store.Chat, error) {
	ctx, cancel := withTimeout(parent)
	defer cancel()
	c, err := loadChat(ctx, d.pool, id, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Chat{}, store.ErrChatNotFound
	}
	if err != nil {
		return store.Chat{}, mapError(parent, err)
	}
	return c, nil
}

func isChatRule(err error) bool {
	return errors.Is(err, store.ErrChatNotFound) || errors.Is(err, store.ErrChatClosed) ||
		errors.Is(err, store.ErrNotConnected) || errors.Is(err, store.ErrValueConflict)
}

// inChatTx блокирует строку чата и проверяет участие. Все изменения одного чата идут последовательно.
func (d *DB) inChatTx(parent context.Context, id [16]byte, userID string, fn func(ctx context.Context, tx pgx.Tx, closed bool) error) error {
	ctx, cancel := withTimeout(parent)
	defer cancel()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return mapError(parent, err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var closed bool
	err = tx.QueryRow(ctx, `SELECT c.closed_at IS NOT NULL FROM chats c
		JOIN chat_participants s ON s.chat_id = c.id AND s.user_id = $2::uuid
		WHERE c.id = $1 FOR UPDATE OF c`, id[:], userID).Scan(&closed)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrChatNotFound
	}
	if err != nil {
		return mapError(parent, err)
	}
	if err := fn(ctx, tx, closed); err != nil {
		if isChatRule(err) {
			return err
		}
		return mapError(parent, err)
	}
	return mapError(parent, tx.Commit(ctx))
}

// SetConnected меняет подключение участника.
func (d *DB) SetConnected(parent context.Context, id [16]byte, userID string, connected bool) (store.Chat, error) {
	var out store.Chat
	err := d.inChatTx(parent, id, userID, func(ctx context.Context, tx pgx.Tx, closed bool) error {
		if closed && connected {
			return store.ErrChatClosed
		}
		if !closed {
			_, err := tx.Exec(ctx, `UPDATE chat_participants SET connected = $3 WHERE chat_id = $1 AND user_id = $2::uuid`, id[:], userID, connected)
			if err != nil {
				return err
			}
		}
		var err error
		out, err = loadChat(ctx, tx, id, userID)
		return err
	})
	if err != nil {
		return store.Chat{}, err
	}
	return out, nil
}

// CloseChat окончательно закрывает чат, отключает обоих и удаляет значения. Повтор ничего не меняет.
func (d *DB) CloseChat(parent context.Context, id [16]byte, userID string) (store.Chat, error) {
	var out store.Chat
	err := d.inChatTx(parent, id, userID, func(ctx context.Context, tx pgx.Tx, closed bool) error {
		if !closed {
			if _, err := tx.Exec(ctx, `UPDATE chats SET closed_at = now() WHERE id = $1`, id[:]); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE chat_participants SET connected = false, public_value = NULL WHERE chat_id = $1`, id[:]); err != nil {
				return err
			}
		}
		var err error
		out, err = loadChat(ctx, tx, id, userID)
		return err
	})
	if err != nil {
		return store.Chat{}, err
	}
	return out, nil
}

// PublishValue сохраняет значение: закрытие, подключение, затем сравнение с уже сохранённым.
func (d *DB) PublishValue(parent context.Context, id [16]byte, userID string, value []byte) error {
	return d.inChatTx(parent, id, userID, func(ctx context.Context, tx pgx.Tx, closed bool) error {
		if closed {
			return store.ErrChatClosed
		}
		var connected bool
		var current []byte
		err := tx.QueryRow(ctx, `SELECT connected, public_value FROM chat_participants WHERE chat_id = $1 AND user_id = $2::uuid`,
			id[:], userID).Scan(&connected, &current)
		if err != nil {
			return err
		}
		switch {
		case !connected:
			return store.ErrNotConnected
		case current == nil:
			_, err := tx.Exec(ctx, `UPDATE chat_participants SET public_value = $3 WHERE chat_id = $1 AND user_id = $2::uuid`, id[:], userID, value)
			return err
		case !bytes.Equal(current, value):
			return store.ErrValueConflict
		}
		return nil
	})
}

// PeerValue возвращает значение собеседника или nil.
func (d *DB) PeerValue(parent context.Context, id [16]byte, userID string) ([]byte, error) {
	ctx, cancel := withTimeout(parent)
	defer cancel()
	var closed bool
	var value []byte
	err := d.pool.QueryRow(ctx, `SELECT c.closed_at IS NOT NULL, p.public_value FROM chats c
		JOIN chat_participants s ON s.chat_id = c.id AND s.user_id = $2::uuid
		JOIN chat_participants p ON p.chat_id = c.id AND p.user_id <> $2::uuid
		WHERE c.id = $1`, id[:], userID).Scan(&closed, &value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrChatNotFound
	}
	if err != nil {
		return nil, mapError(parent, err)
	}
	if closed {
		return nil, store.ErrChatClosed
	}
	return value, nil
}

// closePairChats закрывает активные чаты пары внутри транзакции удаления контакта.
func closePairChats(ctx context.Context, tx pgx.Tx, a, b string) error {
	_, err := tx.Exec(ctx, `WITH closed AS (
		UPDATE chats SET closed_at = now()
		WHERE user_low = LEAST($1::uuid, $2::uuid) AND user_high = GREATEST($1::uuid, $2::uuid) AND closed_at IS NULL
		RETURNING id)
	UPDATE chat_participants SET connected = false, public_value = NULL WHERE chat_id IN (SELECT id FROM closed)`, a, b)
	return err
}
