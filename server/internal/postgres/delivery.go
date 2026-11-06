package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"cryptochat/server/internal/store"
)

// DeliveryState — один запрос по первичным ключам участников. Сами значения не читаются.
func (d *DB) DeliveryState(parent context.Context, id [16]byte, userID string) (store.DeliveryState, error) {
	ctx, cancel := withTimeout(parent)
	defer cancel()
	var st store.DeliveryState
	err := d.pool.QueryRow(ctx, `SELECT p.user_id::text, c.closed_at IS NOT NULL, s.connected,
		s.public_value IS NOT NULL AND p.public_value IS NOT NULL
		FROM chat_participants s
		JOIN chats c ON c.id = s.chat_id
		JOIN chat_participants p ON p.chat_id = s.chat_id AND p.user_id <> s.user_id
		WHERE s.chat_id = $1 AND s.user_id = $2::uuid`, id[:], userID).Scan(&st.PeerID, &st.Closed, &st.Connected, &st.Ready)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.DeliveryState{}, store.ErrChatNotFound
	}
	if err != nil {
		return store.DeliveryState{}, mapError(parent, err)
	}
	return st, nil
}

// PendingQueueCleanup читает закрытые чаты без отметки по частичному индексу.
func (d *DB) PendingQueueCleanup(parent context.Context, limit int) ([]store.ChatMembers, error) {
	ctx, cancel := withTimeout(parent)
	defer cancel()
	rows, err := d.pool.Query(ctx, `SELECT id, user_low::text, user_high::text FROM chats
		WHERE closed_at IS NOT NULL AND queues_deleted_at IS NULL ORDER BY closed_at, id LIMIT $1`, limit)
	if err != nil {
		return nil, mapError(parent, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (store.ChatMembers, error) {
		var c store.ChatMembers
		var id []byte
		if err := r.Scan(&id, &c.UserLow, &c.UserHigh); err != nil {
			return c, err
		}
		copy(c.ID[:], id)
		return c, nil
	})
	if err != nil {
		return nil, mapError(parent, err)
	}
	return out, nil
}

// MarkQueuesDeleted ставит отметку у закрытого чата. Повтор и активный чат ничего не меняют.
func (d *DB) MarkQueuesDeleted(parent context.Context, id [16]byte) error {
	ctx, cancel := withTimeout(parent)
	defer cancel()
	_, err := d.pool.Exec(ctx, `UPDATE chats SET queues_deleted_at = now()
		WHERE id = $1 AND closed_at IS NOT NULL AND queues_deleted_at IS NULL`, id[:])
	return mapError(parent, err)
}

// ReopenQueueCleanup снимает отметку у закрытого чата. Повтор и активный чат ничего не меняют.
func (d *DB) ReopenQueueCleanup(parent context.Context, id [16]byte) error {
	ctx, cancel := withTimeout(parent)
	defer cancel()
	_, err := d.pool.Exec(ctx, `UPDATE chats SET queues_deleted_at = NULL
		WHERE id = $1 AND closed_at IS NOT NULL AND queues_deleted_at IS NOT NULL`, id[:])
	return mapError(parent, err)
}
