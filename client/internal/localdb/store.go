package localdb

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/core/cipher"
)

// Chat — локальная копия чата. Mode и Padding — номера enum контракта (1–7 и 1–4).
type Chat struct {
	ID        [16]byte
	Peer      string
	Creator   string
	Algorithm cipher.Algorithm
	KeyBits   int
	Mode      int
	Padding   int
	CreatedAt time.Time
	ClosedAt  time.Time // нулевое значение — чат активен
}

// Secret — секрет Диффи-Хеллмана чата, ещё не превращённый в ключ.
type Secret struct {
	Private   []byte
	Public    []byte
	Published bool
}

// EnsureProfile записывает владельца новой базы или сверяет его с уже записанным.
func (d *DB) EnsureProfile(address, userID, login string) error {
	var stored string
	err := d.sql.QueryRow(`SELECT value FROM profile WHERE key = 'user_id'`).Scan(&stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return apperr.Profile(d.dir)
	case stored != userID:
		return apperr.Profile(d.dir)
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return apperr.Profile(d.dir)
	}
	defer func() { _ = tx.Rollback() }()
	for k, v := range map[string]string{"server_address": address, "user_id": userID, "login": login} {
		if _, err := tx.Exec(`INSERT INTO profile(key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return apperr.Profile(d.dir)
		}
	}
	if err := tx.Commit(); err != nil {
		return apperr.Profile(d.dir)
	}
	return nil
}

// Owner возвращает идентификатор и логин владельца базы.
func (d *DB) Owner() (userID, login string, err error) {
	if err = d.sql.QueryRow(`SELECT value FROM profile WHERE key = 'user_id'`).Scan(&userID); err != nil {
		return "", "", err
	}
	err = d.sql.QueryRow(`SELECT value FROM profile WHERE key = 'login'`).Scan(&login)
	return userID, login, err
}

// UpsertChat записывает чат или обновляет время сверки. Параметры чата не меняются; закрытый чат не
// становится активным.
func (d *DB) UpsertChat(c Chat) error {
	var closed any
	if !c.ClosedAt.IsZero() {
		closed = c.ClosedAt.UnixMilli()
	}
	_, err := d.sql.Exec(`INSERT INTO chats(chat_id, peer_login, creator_login, algorithm, key_bits, cipher_mode,
			padding_mode, created_at, closed_at, synced_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET
			closed_at = COALESCE(chats.closed_at, excluded.closed_at),
			synced_at = excluded.synced_at`,
		c.ID[:], c.Peer, c.Creator, int(c.Algorithm), c.KeyBits, c.Mode, c.Padding, c.CreatedAt.UnixMilli(), closed,
		d.now().UnixMilli())
	return err
}

// MarkChatClosed отмечает чат закрытым, если он ещё не отмечен.
func (d *DB) MarkChatClosed(id [16]byte) error {
	_, err := d.sql.Exec(`UPDATE chats SET closed_at = ? WHERE chat_id = ? AND closed_at IS NULL`,
		d.now().UnixMilli(), id[:])
	return err
}

const chatColumns = `chat_id, peer_login, creator_login, algorithm, key_bits, cipher_mode, padding_mode, created_at, closed_at`

func scanChat(row interface{ Scan(...any) error }) (Chat, error) {
	var (
		c       Chat
		id      []byte
		alg     int
		created int64
		closed  sql.NullInt64
	)
	if err := row.Scan(&id, &c.Peer, &c.Creator, &alg, &c.KeyBits, &c.Mode, &c.Padding, &created, &closed); err != nil {
		return Chat{}, err
	}
	if len(id) != 16 {
		return Chat{}, fmt.Errorf("длина идентификатора чата %d", len(id))
	}
	copy(c.ID[:], id)
	c.Algorithm = cipher.Algorithm(alg)
	c.CreatedAt = time.UnixMilli(created)
	if closed.Valid {
		c.ClosedAt = time.UnixMilli(closed.Int64)
	}
	return c, nil
}

// KnownActiveChats возвращает чаты, о закрытии которых клиент не знает, новые первыми.
func (d *DB) KnownActiveChats() ([]Chat, error) {
	rows, err := d.sql.Query(`SELECT ` + chatColumns + ` FROM chats WHERE closed_at IS NULL ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Chat возвращает чат по идентификатору.
func (d *DB) Chat(id [16]byte) (Chat, bool, error) {
	c, err := scanChat(d.sql.QueryRow(`SELECT `+chatColumns+` FROM chats WHERE chat_id = ?`, id[:]))
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, false, nil
	}
	return c, err == nil, err
}

// SaveDHSecret сохраняет пару до публикации открытого значения. Строка чата должна существовать.
func (d *DB) SaveDHSecret(id [16]byte, private, public []byte) error {
	_, err := d.sql.Exec(`INSERT INTO dh_secrets(chat_id, private_value, public_value, created_at) VALUES (?, ?, ?, ?)`,
		id[:], private, public, d.now().UnixMilli())
	return err
}

// MarkPublished отмечает подтверждённую публикацию.
func (d *DB) MarkPublished(id [16]byte) error {
	_, err := d.sql.Exec(`UPDATE dh_secrets SET published_at = ? WHERE chat_id = ?`, d.now().UnixMilli(), id[:])
	return err
}

// DHSecret возвращает сохранённую пару чата.
func (d *DB) DHSecret(id [16]byte) (Secret, bool, error) {
	var (
		s   Secret
		pub sql.NullInt64
	)
	err := d.sql.QueryRow(`SELECT private_value, public_value, published_at FROM dh_secrets WHERE chat_id = ?`, id[:]).
		Scan(&s.Private, &s.Public, &pub)
	if errors.Is(err, sql.ErrNoRows) {
		return Secret{}, false, nil
	}
	if err != nil {
		return Secret{}, false, err
	}
	s.Published = pub.Valid
	return s, true, nil
}

// SaveSessionKeyAndDropSecret в одной транзакции записывает ключ длины key_bits/8 и удаляет секрет,
// затем переносит журнал в основной файл, чтобы байты секрета не остались на диске.
func (d *DB) SaveSessionKeyAndDropSecret(id [16]byte, key []byte) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var bits int
	if err := tx.QueryRow(`SELECT key_bits FROM chats WHERE chat_id = ?`, id[:]).Scan(&bits); err != nil {
		return err
	}
	if len(key) != bits/8 {
		return fmt.Errorf("длина ключа %d байт, для чата нужно %d", len(key), bits/8)
	}
	if _, err := tx.Exec(`INSERT INTO session_keys(chat_id, session_key, derived_at) VALUES (?, ?, ?)
		ON CONFLICT(chat_id) DO NOTHING`, id[:], key, d.now().UnixMilli()); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM dh_secrets WHERE chat_id = ?`, id[:]); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return d.checkpoint()
}

// SessionKeyExists сообщает, выработан ли ключ чата.
func (d *DB) SessionKeyExists(id [16]byte) (bool, error) {
	var n int
	err := d.sql.QueryRow(`SELECT count(*) FROM session_keys WHERE chat_id = ?`, id[:]).Scan(&n)
	return n == 1, err
}

// SessionKey возвращает сеансовый ключ чата. Значение не уходит в интерфейс и журналы.
func (d *DB) SessionKey(id [16]byte) ([]byte, bool, error) {
	var k []byte
	err := d.sql.QueryRow(`SELECT session_key FROM session_keys WHERE chat_id = ?`, id[:]).Scan(&k)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return k, err == nil, err
}

// DeleteChatSecrets удаляет секрет и ключ чата и переносит журнал в основной файл; строка чата остаётся.
func (d *DB) DeleteChatSecrets(id [16]byte) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, table := range []string{"dh_secrets", "session_keys"} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE chat_id = ?`, id[:]); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return d.checkpoint()
}

// IsProfileError сообщает, что ошибка относится к недоступной базе профиля.
func IsProfileError(err error) bool {
	return err != nil && (apperr.Is(err, apperr.ProfileStorage, apperr.ProfileBusy) ||
		strings.Contains(err.Error(), "database is closed"))
}
