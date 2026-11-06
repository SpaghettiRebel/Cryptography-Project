package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"cryptochat/server/internal/store"
)

// inPairTx выполняет fn в транзакции. Сначала блокируются строки обоих пользователей по возрастанию id:
// единый порядок исключает взаимные блокировки и делает операции над парой последовательными.
func (d *DB) inPairTx(parent context.Context, userID, otherNorm string, missing error,
	fn func(ctx context.Context, tx pgx.Tx, otherID string) error) error {
	ctx, cancel := withTimeout(parent)
	defer cancel()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return mapError(parent, err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var otherID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM users WHERE login_norm = $1`, otherNorm).Scan(&otherID)
	if errors.Is(err, pgx.ErrNoRows) {
		return missing
	}
	if err != nil {
		return mapError(parent, err)
	}
	if otherID == userID {
		return fn(ctx, tx, otherID)
	}
	rows, err := tx.Query(ctx, `SELECT id FROM users WHERE id = ANY($1::uuid[]) ORDER BY id FOR UPDATE`, []string{userID, otherID})
	if err != nil {
		return mapError(parent, err)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return mapError(parent, err)
	}
	if err := fn(ctx, tx, otherID); err != nil {
		if errors.Is(err, store.ErrSelfRequest) || errors.Is(err, store.ErrAlreadyContact) ||
			errors.Is(err, store.ErrDuplicateRequest) || errors.Is(err, store.ErrRequestNotFound) ||
			errors.Is(err, store.ErrContactNotFound) {
			return err
		}
		return mapError(parent, err)
	}
	return mapError(parent, tx.Commit(ctx))
}

const insertContact = `INSERT INTO contacts (user_low, user_high)
	VALUES (LEAST($1::uuid, $2::uuid), GREATEST($1::uuid, $2::uuid)) ON CONFLICT DO NOTHING`

// SendRequest отправляет запрос или, при встречном запросе, сразу создаёт контакт.
func (d *DB) SendRequest(ctx context.Context, senderID, recipientNorm string) (bool, error) {
	became := false
	err := d.inPairTx(ctx, senderID, recipientNorm, store.ErrNotFound, func(ctx context.Context, tx pgx.Tx, recipientID string) error {
		if recipientID == senderID {
			return store.ErrSelfRequest
		}
		var isContact, outgoing bool
		err := tx.QueryRow(ctx, `SELECT
			EXISTS (SELECT 1 FROM contacts WHERE user_low = LEAST($1::uuid, $2::uuid) AND user_high = GREATEST($1::uuid, $2::uuid)),
			EXISTS (SELECT 1 FROM contact_requests WHERE sender_id = $1::uuid AND recipient_id = $2::uuid)`,
			senderID, recipientID).Scan(&isContact, &outgoing)
		if err != nil {
			return err
		}
		if isContact {
			return store.ErrAlreadyContact
		}
		if outgoing {
			return store.ErrDuplicateRequest
		}
		tag, err := tx.Exec(ctx, `DELETE FROM contact_requests WHERE sender_id = $1::uuid AND recipient_id = $2::uuid`, recipientID, senderID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			became = true
			_, err = tx.Exec(ctx, insertContact, senderID, recipientID)
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO contact_requests (sender_id, recipient_id) VALUES ($1::uuid, $2::uuid)`, senderID, recipientID)
		return err
	})
	if err != nil {
		return false, err
	}
	return became, nil
}

// AcceptRequest принимает входящий запрос.
func (d *DB) AcceptRequest(ctx context.Context, recipientID, senderNorm string) error {
	return d.inPairTx(ctx, recipientID, senderNorm, store.ErrRequestNotFound, func(ctx context.Context, tx pgx.Tx, senderID string) error {
		tag, err := tx.Exec(ctx, `DELETE FROM contact_requests WHERE sender_id = $1::uuid AND recipient_id = $2::uuid`, senderID, recipientID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return store.ErrRequestNotFound
		}
		_, err = tx.Exec(ctx, insertContact, senderID, recipientID)
		return err
	})
}

// RejectRequest отклоняет входящий запрос.
func (d *DB) RejectRequest(ctx context.Context, recipientID, senderNorm string) error {
	return d.inPairTx(ctx, recipientID, senderNorm, store.ErrRequestNotFound, func(ctx context.Context, tx pgx.Tx, senderID string) error {
		tag, err := tx.Exec(ctx, `DELETE FROM contact_requests WHERE sender_id = $1::uuid AND recipient_id = $2::uuid`, senderID, recipientID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return store.ErrRequestNotFound
		}
		return nil
	})
}

// RemoveContact удаляет контакт у обоих.
func (d *DB) RemoveContact(ctx context.Context, userID, otherNorm string) error {
	return d.inPairTx(ctx, userID, otherNorm, store.ErrContactNotFound, func(ctx context.Context, tx pgx.Tx, otherID string) error {
		tag, err := tx.Exec(ctx, `DELETE FROM contacts WHERE user_low = LEAST($1::uuid, $2::uuid) AND user_high = GREATEST($1::uuid, $2::uuid)`, userID, otherID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return store.ErrContactNotFound
		}
		return nil
	})
}

func (d *DB) refs(parent, ctx context.Context, query, userID string) ([]store.UserRef, error) {
	rows, err := d.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, mapError(parent, err)
	}
	refs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (store.UserRef, error) {
		var u store.UserRef
		err := r.Scan(&u.ID, &u.Login)
		return u, err
	})
	if err != nil {
		return nil, mapError(parent, err)
	}
	return refs, nil
}

// ListContacts возвращает списки, отсортированные по логину.
func (d *DB) ListContacts(parent context.Context, userID string) (store.ContactLists, error) {
	ctx, cancel := withTimeout(parent)
	defer cancel()
	var out store.ContactLists
	var err error
	out.Contacts, err = d.refs(parent, ctx, `
		SELECT u.id::text, u.login FROM contacts c
		JOIN users u ON u.id = CASE WHEN c.user_low = $1::uuid THEN c.user_high ELSE c.user_low END
		WHERE c.user_low = $1::uuid OR c.user_high = $1::uuid ORDER BY u.login_norm`, userID)
	if err != nil {
		return store.ContactLists{}, err
	}
	out.Incoming, err = d.refs(parent, ctx, `
		SELECT u.id::text, u.login FROM contact_requests r JOIN users u ON u.id = r.sender_id
		WHERE r.recipient_id = $1::uuid ORDER BY u.login_norm`, userID)
	if err != nil {
		return store.ContactLists{}, err
	}
	out.Outgoing, err = d.refs(parent, ctx, `
		SELECT u.id::text, u.login FROM contact_requests r JOIN users u ON u.id = r.recipient_id
		WHERE r.sender_id = $1::uuid ORDER BY u.login_norm`, userID)
	if err != nil {
		return store.ContactLists{}, err
	}
	return out, nil
}
