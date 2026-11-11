package localdb

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"

	"cryptochat/core/protocol"
)

// Направление сообщения.
const (
	DirOut = 1
	DirIn  = 2
)

// Вид сообщения.
const (
	KindPending = 0 // входящее до расшифрования
	KindText    = 1
	KindFile    = 2
	KindImage   = 3
	KindControl = 4 // служебное, в истории не показывается
)

// Состояния сообщения. Числа хранятся в базе и не меняются.
const (
	StateQueued           = 1
	StateSending          = 2
	StateWaiting          = 3
	StateSent             = 4
	StateCancelled        = 5
	StateFailed           = 6
	StateUndelivered      = 7
	StateReceiving        = 10
	StateDecryptPending   = 11
	StateDecrypting       = 12
	StateDecryptCancelled = 13
	StateDone             = 14
	StateCancelledByPeer  = 15
	StateCorrupted        = 16
	StateKeyMissing       = 17
)

// ChunkSize — размер куска открытого содержимого; кусок idx лежит по смещению idx·ChunkSize.
const ChunkSize = 49152

// BatchRows — сколько строк фрагментов или кусков читается за один запрос.
const BatchRows = 16

// ErrStale — сообщение уже не в том состоянии, в котором запись имеет смысл (например, отменено).
var ErrStale = errors.New("localdb: сообщение уже в другом состоянии")

// Final сообщает, что состояние конечное.
func Final(state int) bool {
	switch state {
	case StateSent, StateCancelled, StateFailed, StateUndelivered, StateDone, StateCancelledByPeer, StateCorrupted, StateKeyMissing:
		return true
	}
	return false
}

// keepsChunks сообщает, что в состоянии открытое содержимое файла остаётся.
func keepsChunks(state int) bool {
	switch state {
	case StateQueued, StateSending, StateWaiting, StateSent, StateDone, StateDecrypting:
		return true
	}
	return false
}

// Message — строка таблицы messages. Нулевые значения времени и Size = -1 означают NULL.
type Message struct {
	Seq            int64
	ChatID         [16]byte
	MessageID      [16]byte
	Direction      int
	Kind           int
	State          int
	ErrorCode      string
	CreatedAt      int64
	CompletedAt    int64
	ReadAt         int64
	Text           string
	FileName       string
	FileType       string
	MediaType      string
	Size           int64
	Total          int
	EncryptedCount int
	SentCount      int
	ReceivedCount  int
	SourcePath     string
	SourceSize     int64
	SourceMtime    int64
	ChunksComplete bool
}

const messageColumns = `seq, chat_id, message_id, direction, kind, state, error_code, created_at, completed_at, read_at,
	text, file_name, file_type, media_type, size, total, encrypted_count, sent_count, received_count,
	source_path, source_size, source_mtime, chunks_complete`

func scanMessage(row interface{ Scan(...any) error }) (Message, error) {
	var (
		m                                         Message
		chat, id                                  []byte
		code, text, name, ftype, media, source    sql.NullString
		completed, read, size, total, ssize, mtim sql.NullInt64
		complete                                  int
	)
	if err := row.Scan(&m.Seq, &chat, &id, &m.Direction, &m.Kind, &m.State, &code, &m.CreatedAt, &completed, &read,
		&text, &name, &ftype, &media, &size, &total, &m.EncryptedCount, &m.SentCount, &m.ReceivedCount,
		&source, &ssize, &mtim, &complete); err != nil {
		return Message{}, err
	}
	if len(chat) != 16 || len(id) != 16 {
		return Message{}, fmt.Errorf("длина идентификатора в сообщении %d", m.Seq)
	}
	copy(m.ChatID[:], chat)
	copy(m.MessageID[:], id)
	m.ErrorCode, m.Text, m.FileName, m.FileType, m.MediaType, m.SourcePath = code.String, text.String, name.String,
		ftype.String, media.String, source.String
	m.CompletedAt, m.ReadAt, m.Total, m.SourceSize, m.SourceMtime = completed.Int64, read.Int64, int(total.Int64),
		ssize.Int64, mtim.Int64
	m.Size = -1
	if size.Valid {
		m.Size = size.Int64
	}
	m.ChunksComplete = complete == 1
	return m, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// CreateOutgoing записывает исходящее сообщение в состоянии QUEUED и возвращает его номер. firstChunk,
// если не nil, сразу записывается куском 0 (служебное сообщение).
func (d *DB) CreateOutgoing(m Message, firstChunk []byte) (int64, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	now := d.now().UnixMilli()
	var size any
	if m.Size >= 0 {
		size = m.Size
	}
	complete := 0
	if m.ChunksComplete {
		complete = 1
	}
	r, err := tx.Exec(`INSERT INTO messages(chat_id, message_id, direction, kind, state, created_at, read_at, text,
			file_name, file_type, size, total, source_path, source_size, source_mtime, chunks_complete)
		VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ChatID[:], m.MessageID[:], m.Kind, StateQueued, now, now, nullString(m.Text), nullString(m.FileName),
		nullString(m.FileType), size, m.Total, nullString(m.SourcePath), nullInt(m.SourceSize), nullInt(m.SourceMtime), complete)
	if err != nil {
		return 0, err
	}
	seq, err := r.LastInsertId()
	if err != nil {
		return 0, err
	}
	if firstChunk != nil {
		if _, err := tx.Exec(`INSERT INTO message_chunks(seq, idx, data) VALUES (?, 0, ?)`, seq, firstChunk); err != nil {
			return 0, err
		}
	}
	return seq, tx.Commit()
}

// Message возвращает сообщение по номеру.
func (d *DB) Message(seq int64) (Message, bool, error) {
	m, err := scanMessage(d.sql.QueryRow(`SELECT `+messageColumns+` FROM messages WHERE seq = ?`, seq))
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, nil
	}
	return m, err == nil, err
}

func (d *DB) queryMessages(query string, args ...any) ([]Message, error) {
	rows, err := d.sql.Query(`SELECT `+messageColumns+` FROM messages `+query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MessageByID ищет сообщение чата по направлению и идентификатору.
func (d *DB) MessageByID(chat [16]byte, dir int, id [16]byte) (Message, bool, error) {
	list, err := d.queryMessages(`WHERE chat_id = ? AND direction = ? AND message_id = ?`, chat[:], dir, id[:])
	if err != nil || len(list) == 0 {
		return Message{}, false, err
	}
	return list[0], true, nil
}

// cleanup удаляет данные, которых не должно быть в состоянии to; wiped — удалены куски открытого
// содержимого.
func cleanup(tx *sql.Tx, seq int64, to int) (wiped bool, err error) {
	if Final(to) {
		for _, table := range []string{"in_fragments", "out_fragments"} {
			if _, err := tx.Exec(`DELETE FROM `+table+` WHERE seq = ?`, seq); err != nil {
				return false, err
			}
		}
	}
	if !keepsChunks(to) {
		r, err := tx.Exec(`DELETE FROM message_chunks WHERE seq = ?`, seq)
		if err != nil {
			return false, err
		}
		n, _ := r.RowsAffected()
		wiped = n > 0
	}
	return wiped, nil
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// transitionTx переводит сообщение из одного из состояний from в to с кодом причины (пусто — без кода)
// и удаляет лишние данные; возвращает false, если сообщение в другом состоянии, и признак удаления
// кусков открытого содержимого.
func (d *DB) transitionTx(tx *sql.Tx, seq int64, from []int, to int, code string) (ok, wiped bool, err error) {
	args := []any{to, nullString(code)}
	set := `state = ?, error_code = ?`
	if Final(to) {
		set += `, completed_at = ?, source_path = NULL`
		args = append(args, d.now().UnixMilli())
	}
	args = append(args, seq)
	for _, f := range from {
		args = append(args, f)
	}
	r, err := tx.Exec(`UPDATE messages SET `+set+` WHERE seq = ? AND state IN (`+placeholders(len(from))+`)`, args...)
	if err != nil {
		return false, false, err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return false, false, nil
	}
	wiped, err = cleanup(tx, seq, to)
	return err == nil, wiped, err
}

// afterWipe переносит журнал WAL в основной файл после удаления открытого содержимого: со
// secure_delete удалённые страницы затёрты, а старые кадры журнала с содержимым исчезают.
func (d *DB) afterWipe(wiped bool) {
	if wiped {
		_ = d.checkpoint()
	}
}

// Transition переводит сообщение из одного из состояний from в to одной транзакцией. Если удалены куски
// открытого содержимого, журнал WAL переносится в основной файл.
func (d *DB) Transition(seq int64, from []int, to int, code string) (bool, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	ok, wiped, err := d.transitionTx(tx, seq, from, to, code)
	if err != nil || !ok {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	d.afterWipe(wiped)
	return true, nil
}

// SetWaiting ставит исходящему состояние WAITING с причиной.
func (d *DB) SetWaiting(seq int64, code string) (bool, error) {
	r, err := d.sql.Exec(`UPDATE messages SET state = ?, error_code = ? WHERE seq = ? AND state IN (?, ?, ?)`,
		StateWaiting, nullString(code), seq, StateQueued, StateSending, StateWaiting)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n == 1, nil
}

// AddOutFragment записывает зашифрованный фрагмент idx к отправке и увеличивает encrypted_count.
// Номер меньше encrypted_count уже записан и пропускается. Возвращает false, если сообщение больше не
// отправляется.
func (d *DB) AddOutFragment(seq int64, idx int, wire []byte) (bool, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var state, enc int
	if err := tx.QueryRow(`SELECT state, encrypted_count FROM messages WHERE seq = ?`, seq).Scan(&state, &enc); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if state != StateQueued && state != StateSending && state != StateWaiting {
		return false, nil
	}
	if idx < enc {
		return true, nil
	}
	if idx != enc {
		return false, fmt.Errorf("фрагмент %d после %d", idx, enc)
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO out_fragments(seq, idx, wire) VALUES (?, ?, ?)`, seq, idx, wire); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE messages SET encrypted_count = ?, state = CASE state WHEN ? THEN ? ELSE state END WHERE seq = ?`,
		idx+1, StateQueued, StateSending, seq); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// OutFragment возвращает фрагмент к отправке.
func (d *DB) OutFragment(seq int64, idx int) ([]byte, bool, error) {
	var wire []byte
	err := d.sql.QueryRow(`SELECT wire FROM out_fragments WHERE seq = ? AND idx = ?`, seq, idx).Scan(&wire)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return wire, err == nil, err
}

// FragmentSent отмечает успешную отправку фрагмента idx: строка спула удаляется, sent_count растёт, причина
// ожидания снимается; после последнего — SENT. Возвращает сообщение после записи и false, если сообщение
// уже не отправляется.
func (d *DB) FragmentSent(seq int64, idx int) (Message, bool, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return Message{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	r, err := tx.Exec(`UPDATE messages SET sent_count = sent_count + 1, state = ?, error_code = NULL
		WHERE seq = ? AND state IN (?, ?) AND sent_count = ?`, StateSending, seq, StateSending, StateWaiting, idx)
	if err != nil {
		return Message{}, false, err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return Message{}, false, nil
	}
	if _, err := tx.Exec(`DELETE FROM out_fragments WHERE seq = ? AND idx = ?`, seq, idx); err != nil {
		return Message{}, false, err
	}
	var sent, total int
	if err := tx.QueryRow(`SELECT sent_count, COALESCE(total, 0) FROM messages WHERE seq = ?`, seq).Scan(&sent, &total); err != nil {
		return Message{}, false, err
	}
	if sent >= total {
		if _, _, err := d.transitionTx(tx, seq, []int{StateSending}, StateSent, ""); err != nil {
			return Message{}, false, err
		}
	}
	m, err := scanMessage(tx.QueryRow(`SELECT `+messageColumns+` FROM messages WHERE seq = ?`, seq))
	if err != nil {
		return Message{}, false, err
	}
	return m, true, tx.Commit()
}

// Incoming — принятый фрагмент: байты как пришли и разобранный фрагмент (nil — не разобран).
type Incoming struct {
	Wire []byte
	Frag *protocol.Fragment
}

// Received — итог записи фрагмента: номер сообщения (0 — неизвестно), отброшен ли фрагмент и стали ли
// после него получены все части.
type Received struct {
	Seq       int64
	Dropped   bool
	Corrupted bool
	Completed bool
}

// ReceiveBatch записывает пачку фрагментов чата одной транзакцией. Фрагменты сообщений в конечном
// состоянии, чужие, неразобранные и с недопустимым числом частей не записываются: их можно подтверждать
// и отбрасывать. Сообщение с несовместимой частью получает CORRUPTED.
func (d *DB) ReceiveBatch(chat [16]byte, in []Incoming, maxTotal uint32) ([]Received, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now := d.now().UnixMilli()
	out := make([]Received, len(in))
	corrupt := func(seq int64) (bool, error) {
		ok, _, err := d.transitionTx(tx, seq, []int{StateReceiving, StateDecryptPending, StateDecryptCancelled}, StateCorrupted, "MESSAGE_CORRUPTED")
		return ok, err
	}
	for i, it := range in {
		f := it.Frag
		if f == nil {
			out[i] = Received{Dropped: true}
			continue
		}
		var (
			seq           int64
			state, total  int
			received      int
			exists        = true
			foreignOrSize = f.ChatID != chat || f.Total > maxTotal
		)
		err := tx.QueryRow(`SELECT seq, state, COALESCE(total, 0), received_count FROM messages
			WHERE chat_id = ? AND direction = 2 AND message_id = ?`, chat[:], f.MessageID[:]).Scan(&seq, &state, &total, &received)
		if errors.Is(err, sql.ErrNoRows) {
			exists = false
		} else if err != nil {
			return nil, err
		}
		switch {
		case exists && Final(state):
			out[i] = Received{Seq: seq, Dropped: true}
			continue
		case foreignOrSize || (exists && total != 0 && uint32(total) != f.Total):
			r := Received{Seq: seq, Dropped: true}
			if exists {
				ok, err := corrupt(seq)
				if err != nil {
					return nil, err
				}
				r.Corrupted = ok
			}
			out[i] = r
			continue
		case !exists:
			res, err := tx.Exec(`INSERT INTO messages(chat_id, message_id, direction, kind, state, created_at, total)
				VALUES (?, ?, 2, 0, ?, ?, ?)`, chat[:], f.MessageID[:], StateReceiving, now, f.Total)
			if err != nil {
				return nil, err
			}
			if seq, err = res.LastInsertId(); err != nil {
				return nil, err
			}
			state, total = StateReceiving, int(f.Total)
		}
		out[i] = Received{Seq: seq}
		if state != StateReceiving {
			continue // все части уже есть: повтор
		}
		res, err := tx.Exec(`INSERT OR IGNORE INTO in_fragments(seq, idx, wire) VALUES (?, ?, ?)`, seq, f.Index, it.Wire)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		received++
		if received >= total {
			if _, err := tx.Exec(`UPDATE messages SET received_count = ?, state = ? WHERE seq = ?`, received, StateDecryptPending, seq); err != nil {
				return nil, err
			}
			out[i].Completed = true
		} else if _, err := tx.Exec(`UPDATE messages SET received_count = ? WHERE seq = ?`, received, seq); err != nil {
			return nil, err
		}
	}
	return out, tx.Commit()
}

// Fragment — строка in_fragments.
type Fragment struct {
	Idx  int
	Wire []byte
}

// InFragments читает принятые фрагменты с номером больше after, не больше limit строк.
func (d *DB) InFragments(seq int64, after, limit int) ([]Fragment, error) {
	rows, err := d.sql.Query(`SELECT idx, wire FROM in_fragments WHERE seq = ? AND idx > ? ORDER BY idx LIMIT ?`, seq, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fragment
	for rows.Next() {
		var f Fragment
		if err := rows.Scan(&f.Idx, &f.Wire); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Chunk — кусок открытого содержимого.
type Chunk struct {
	Idx  int
	Data []byte
}

// Chunks читает куски с номером больше after, не больше limit строк.
func (d *DB) Chunks(seq int64, after, limit int) ([]Chunk, error) {
	rows, err := d.sql.Query(`SELECT idx, data FROM message_chunks WHERE seq = ? AND idx > ? ORDER BY idx LIMIT ?`, seq, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.Idx, &c.Data); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PutChunk записывает кусок idx, пока сообщение отправляется или расшифровывается; complete отмечает,
// что копия содержимого полная.
func (d *DB) PutChunk(seq int64, idx int, data []byte, complete bool) error {
	if len(data) > ChunkSize {
		return fmt.Errorf("кусок %d байт", len(data))
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	r, err := tx.Exec(`INSERT OR REPLACE INTO message_chunks(seq, idx, data)
		SELECT ?, ?, ? WHERE EXISTS (SELECT 1 FROM messages WHERE seq = ? AND state IN (?, ?, ?, ?))`,
		seq, idx, data, seq, StateQueued, StateSending, StateWaiting, StateDecrypting)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrStale
	}
	if complete {
		if _, err := tx.Exec(`UPDATE messages SET chunks_complete = 1 WHERE seq = ?`, seq); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MarkChunksComplete отмечает полную копию содержимого (например, пустого файла).
func (d *DB) MarkChunksComplete(seq int64) error {
	_, err := d.sql.Exec(`UPDATE messages SET chunks_complete = 1 WHERE seq = ?`, seq)
	return err
}

// chunkWriter пишет куски по смещению; смещение должно быть кратно ChunkSize.
type chunkWriter struct {
	d   *DB
	seq int64
}

// ChunkWriterAt возвращает io.WriterAt над кусками сообщения: каждый вызов пишет один кусок.
func (d *DB) ChunkWriterAt(seq int64) io.WriterAt {
	return chunkWriter{d: d, seq: seq}
}

func (w chunkWriter) WriteAt(p []byte, off int64) (int, error) {
	if off%ChunkSize != 0 || off < 0 {
		return 0, fmt.Errorf("смещение %d не кратно куску", off)
	}
	if len(p) > ChunkSize {
		return 0, fmt.Errorf("кусок %d байт", len(p))
	}
	if err := w.d.PutChunk(w.seq, int(off/ChunkSize), p, false); err != nil {
		return 0, err
	}
	return len(p), nil
}

// DeleteChunks удаляет открытое содержимое сообщения.
func (d *DB) DeleteChunks(seq int64) error {
	_, err := d.sql.Exec(`DELETE FROM message_chunks WHERE seq = ?`, seq)
	return err
}

// FinishIncoming завершает расшифрование: удаляет принятые фрагменты и ставит DONE с видом и полями.
// Для текста и служебного сообщения куски удаляются. Возвращает false, если расшифрование уже отменено.
func (d *DB) FinishIncoming(seq int64, kind int, text, name, fileType, media string, size int64) (bool, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var read any
	if kind == KindControl {
		read = d.now().UnixMilli()
	}
	r, err := tx.Exec(`UPDATE messages SET state = ?, error_code = NULL, kind = ?, text = ?, file_name = ?, file_type = ?,
			media_type = ?, size = ?, completed_at = ?, read_at = ? WHERE seq = ? AND state = ?`,
		StateDone, kind, nullString(text), nullString(name), nullString(fileType), nullString(media), size,
		d.now().UnixMilli(), read, seq, StateDecrypting)
	if err != nil {
		return false, err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return false, nil
	}
	if _, err := tx.Exec(`DELETE FROM in_fragments WHERE seq = ?`, seq); err != nil {
		return false, err
	}
	if kind == KindText || kind == KindControl {
		if _, err := tx.Exec(`DELETE FROM message_chunks WHERE seq = ?`, seq); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// CancelledByPeer отмечает входящее сообщение чата отменённым отправителем. Если сообщения ещё нет,
// создаётся строка-признак, чтобы части, пришедшие позже, отбрасывались. Возвращает номер и признак
// изменения.
func (d *DB) CancelledByPeer(chat, id [16]byte) (int64, bool, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var seq int64
	err = tx.QueryRow(`SELECT seq FROM messages WHERE chat_id = ? AND direction = 2 AND message_id = ?`, chat[:], id[:]).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		now := d.now().UnixMilli()
		r, err := tx.Exec(`INSERT INTO messages(chat_id, message_id, direction, kind, state, created_at, completed_at)
			VALUES (?, ?, 2, 0, ?, ?, ?)`, chat[:], id[:], StateCancelledByPeer, now, now)
		if err != nil {
			return 0, false, err
		}
		if seq, err = r.LastInsertId(); err != nil {
			return 0, false, err
		}
		return seq, true, tx.Commit()
	}
	if err != nil {
		return 0, false, err
	}
	ok, wiped, err := d.transitionTx(tx, seq, []int{StateReceiving, StateDecryptPending, StateDecrypting, StateDecryptCancelled},
		StateCancelledByPeer, "")
	if err != nil || !ok {
		return seq, false, err
	}
	if err := tx.Commit(); err != nil {
		return seq, false, err
	}
	d.afterWipe(wiped)
	return seq, true, nil
}

// ResetDecrypting возвращает прерванные расшифрования в DECRYPT_PENDING и удаляет частичные куски
// вместе со старыми кадрами журнала WAL.
func (d *DB) ResetDecrypting() error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	r, err := tx.Exec(`DELETE FROM message_chunks WHERE seq IN (SELECT seq FROM messages WHERE state = ?)`, StateDecrypting)
	if err != nil {
		return err
	}
	wiped, _ := r.RowsAffected()
	if _, err := tx.Exec(`UPDATE messages SET state = ? WHERE state = ?`, StateDecryptPending, StateDecrypting); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	d.afterWipe(wiped > 0)
	return nil
}

func (d *DB) seqs(query string, args ...any) ([]int64, error) {
	rows, err := d.sql.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var s int64
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// PendingDecrypt возвращает сообщения, ждущие расшифрования без причины ожидания.
func (d *DB) PendingDecrypt() ([]int64, error) {
	return d.seqs(`SELECT seq FROM messages WHERE state = ? AND error_code IS NULL ORDER BY seq`, StateDecryptPending)
}

// OutgoingChats возвращает чаты с незавершёнными исходящими.
func (d *DB) OutgoingChats() ([][16]byte, error) {
	rows, err := d.sql.Query(`SELECT DISTINCT chat_id FROM messages WHERE direction = 1 AND state IN (?, ?, ?)`,
		StateQueued, StateSending, StateWaiting)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][16]byte
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var id [16]byte
		copy(id[:], b)
		out = append(out, id)
	}
	return out, rows.Err()
}

// NextOutgoing возвращает следующее сообщение чата к передаче: служебные впереди, затем по номеру.
func (d *DB) NextOutgoing(chat [16]byte) (Message, bool, error) {
	list, err := d.queryMessages(`WHERE chat_id = ? AND direction = 1 AND state IN (?, ?, ?)
		ORDER BY (kind = 4) DESC, seq LIMIT 1`, chat[:], StateQueued, StateSending, StateWaiting)
	if err != nil || len(list) == 0 {
		return Message{}, false, err
	}
	return list[0], true, nil
}

// Unfinished возвращает незавершённые сообщения чата.
func (d *DB) Unfinished(chat [16]byte) ([]Message, error) {
	return d.queryMessages(`WHERE chat_id = ? AND state IN (?, ?, ?, ?, ?, ?, ?) ORDER BY seq`, chat[:],
		StateQueued, StateSending, StateWaiting, StateReceiving, StateDecryptPending, StateDecrypting, StateDecryptCancelled)
}

// visible — условие видимого в истории сообщения: не служебное и не строка-признак отмены, пришедшей
// раньше частей (вид не известен, частей не было: total пуст).
const visible = `kind != 4 AND NOT (kind = 0 AND state = 15 AND total IS NULL)`

// ListMessages возвращает до limit видимых сообщений чата с номером меньше before (0 — последние) по
// возрастанию номера и признак, что есть более ранние.
func (d *DB) ListMessages(chat [16]byte, before int64, limit int) ([]Message, bool, error) {
	if before <= 0 {
		before = 1<<62 - 1
	}
	list, err := d.queryMessages(`WHERE chat_id = ? AND `+visible+` AND seq < ? ORDER BY seq DESC LIMIT ?`,
		chat[:], before, limit+1)
	if err != nil {
		return nil, false, err
	}
	more := len(list) > limit
	if more {
		list = list[:limit]
	}
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	return list, more, nil
}

// UnreadCounts — число непросмотренных входящих готовых сообщений по чатам.
func (d *DB) UnreadCounts() (map[[16]byte]int, error) {
	rows, err := d.sql.Query(`SELECT chat_id, count(*) FROM messages
		WHERE direction = 2 AND state = ? AND read_at IS NULL AND kind IN (1, 2, 3) GROUP BY chat_id`, StateDone)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[16]byte]int{}
	for rows.Next() {
		var (
			b []byte
			n int
		)
		if err := rows.Scan(&b, &n); err != nil {
			return nil, err
		}
		var id [16]byte
		copy(id[:], b)
		out[id] = n
	}
	return out, rows.Err()
}

// MarkRead отмечает готовые входящие сообщения чата просмотренными; возвращает число отмеченных.
func (d *DB) MarkRead(chat [16]byte) (int64, error) {
	r, err := d.sql.Exec(`UPDATE messages SET read_at = ? WHERE chat_id = ? AND direction = 2 AND state = ? AND read_at IS NULL`,
		d.now().UnixMilli(), chat[:], StateDone)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// ClosedChat — закрытый чат и число сообщений в его истории.
type ClosedChat struct {
	Chat
	MessageCount int
}

// ClosedChats возвращает закрытые чаты, недавно закрытые первыми.
func (d *DB) ClosedChats() ([]ClosedChat, error) {
	rows, err := d.sql.Query(`SELECT ` + chatColumns + ` FROM chats WHERE closed_at IS NOT NULL ORDER BY closed_at DESC`)
	if err != nil {
		return nil, err
	}
	var out []ClosedChat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, ClosedChat{Chat: c})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// Курсор закрыт до следующих запросов: соединение одно.
	for i := range out {
		if err := d.sql.QueryRow(`SELECT count(*) FROM messages WHERE chat_id = ? AND `+visible, out[i].ID[:]).
			Scan(&out[i].MessageCount); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// DeleteChatHistory удаляет сообщения чата вместе с кусками и фрагментами; строка чата остаётся.
func (d *DB) DeleteChatHistory(chat [16]byte) error {
	if _, err := d.sql.Exec(`DELETE FROM messages WHERE chat_id = ?`, chat[:]); err != nil {
		return err
	}
	_ = d.checkpoint()
	return nil
}

// SetWanted записывает желание держать поток приёма чата открытым.
func (d *DB) SetWanted(chat [16]byte, wanted bool) error {
	v := 0
	if wanted {
		v = 1
	}
	_, err := d.sql.Exec(`INSERT INTO chat_receive(chat_id, wanted, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET wanted = excluded.wanted, updated_at = excluded.updated_at`,
		chat[:], v, d.now().UnixMilli())
	return err
}

// Wanted возвращает желание принимать чат; ok = false, если строки нет.
func (d *DB) Wanted(chat [16]byte) (wanted, ok bool, err error) {
	var v int
	err = d.sql.QueryRow(`SELECT wanted FROM chat_receive WHERE chat_id = ?`, chat[:]).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	return v == 1, err == nil, err
}
