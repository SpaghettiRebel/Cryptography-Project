package localdb

import (
	"bytes"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"cryptochat/core/protocol"
)

func msgID(b byte) [16]byte { return [16]byte{0xEE, b} }

func frag(chat [16]byte, id [16]byte, idx, total uint32) Incoming {
	f := &protocol.Fragment{ChatID: chat, MessageID: id, Index: idx, Total: total, Cipher: []byte{byte(idx), 1, 2}}
	w, _ := f.Marshal()
	return Incoming{Wire: w, Frag: f}
}

// База 006 с данными получает таблицы переписки, данные остаются, повторное открытие ничего не меняет.
func TestMigrationOver006(t *testing.T) {
	path := tempPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+url.Values{"_pragma": {"foreign_keys(ON)"}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	v1, err := migrations.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{string(v1), `INSERT INTO schema_migrations VALUES (1, 1)`,
		`INSERT INTO profile VALUES ('login', 'alice')`,
		`INSERT INTO chats VALUES (x'01000000000000000000000000000000', 'bob', 'alice', 2, 256, 2, 3, 5, NULL, 6)`} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = raw.Close()

	db := open(t, path)
	if _, ok, err := db.Chat([16]byte{1}); !ok || err != nil {
		t.Fatalf("чат 006 потерян: %v", err)
	}
	var n int
	_ = db.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN
		('messages', 'message_chunks', 'out_fragments', 'in_fragments', 'chat_receive')`).Scan(&n)
	if n != 5 {
		t.Fatalf("таблиц переписки %d", n)
	}
	seq, err := db.CreateOutgoing(Message{ChatID: [16]byte{1}, MessageID: msgID(1), Kind: KindText, Text: "привет", Total: 1, Size: 12}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	db = open(t, path)
	m, ok, err := db.Message(seq)
	if !ok || err != nil || m.Text != "привет" || m.State != StateQueued || m.Direction != DirOut || m.ReadAt == 0 {
		t.Fatalf("после переоткрытия %+v %v", m, err)
	}
	_ = db.sql.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n)
	if n != 2 {
		t.Fatalf("миграций %d", n)
	}
}

func TestMessageConstraints(t *testing.T) {
	db := open(t, tempPath(t))
	c := chat(1, 128)
	if err := db.UpsertChat(c); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO messages(chat_id, message_id, direction, kind, state, created_at) VALUES (x'01000000000000000000000000000000', x'01', 1, 1, 1, 0)`,
		`INSERT INTO messages(chat_id, message_id, direction, kind, state, created_at) VALUES (x'01000000000000000000000000000000', x'01000000000000000000000000000000', 3, 1, 1, 0)`,
		`INSERT INTO messages(chat_id, message_id, direction, kind, state, created_at) VALUES (x'01000000000000000000000000000000', x'01000000000000000000000000000000', 1, 5, 1, 0)`,
		`INSERT INTO messages(chat_id, message_id, direction, kind, state, created_at) VALUES (x'09000000000000000000000000000000', x'01000000000000000000000000000000', 1, 1, 1, 0)`,
		`INSERT INTO in_fragments(seq, idx, wire) VALUES (999, 0, x'00')`,
	} {
		if _, err := db.sql.Exec(q); err == nil {
			t.Errorf("ограничение не сработало: %.90s", q)
		}
	}
	if _, err := db.CreateOutgoing(Message{ChatID: c.ID, MessageID: msgID(1), Kind: KindText, Total: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateOutgoing(Message{ChatID: c.ID, MessageID: msgID(1), Kind: KindText, Total: 1}, nil); err == nil {
		t.Fatal("повтор message_id принят")
	}
	if err := db.PutChunk(1, 0, make([]byte, ChunkSize+1), false); err == nil {
		t.Fatal("кусок больше 48 КиБ принят")
	}
}

func TestOutgoingSpool(t *testing.T) {
	db := open(t, tempPath(t))
	c := chat(1, 128)
	_ = db.UpsertChat(c)
	seq, err := db.CreateOutgoing(Message{ChatID: c.ID, MessageID: msgID(1), Kind: KindFile, FileName: "a.bin", Total: 2, Size: 70000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if ok, err := db.AddOutFragment(seq, i, []byte{byte(i)}); !ok || err != nil {
			t.Fatal(ok, err)
		}
	}
	if ok, err := db.AddOutFragment(seq, 0, []byte{9}); !ok || err != nil {
		t.Fatal("повтор уже записанного номера должен пропускаться", ok, err)
	}
	m, _, _ := db.Message(seq)
	if m.State != StateSending || m.EncryptedCount != 2 {
		t.Fatalf("%+v", m)
	}
	if ok, _ := db.SetWaiting(seq, "QUEUE_FULL"); !ok {
		t.Fatal("WAITING")
	}
	if _, ok, _ := db.FragmentSent(seq, 1); ok {
		t.Fatal("отправка не по порядку принята")
	}
	m, ok, err := db.FragmentSent(seq, 0)
	if !ok || err != nil || m.SentCount != 1 || m.State != StateSending || m.ErrorCode != "" {
		t.Fatalf("%+v %v", m, err)
	}
	if _, ok, _ := db.OutFragment(seq, 0); ok {
		t.Fatal("отправленная часть осталась в спуле")
	}
	m, _, _ = db.FragmentSent(seq, 1)
	if m.State != StateSent || m.CompletedAt == 0 {
		t.Fatalf("%+v", m)
	}
	var n int
	_ = db.sql.QueryRow(`SELECT count(*) FROM out_fragments`).Scan(&n)
	if n != 0 {
		t.Fatal("спул не пуст")
	}
	// Отменённое сообщение больше не принимает части и куски.
	seq2, _ := db.CreateOutgoing(Message{ChatID: c.ID, MessageID: msgID(2), Kind: KindFile, Total: 3, Size: 100000}, nil)
	_ = db.PutChunk(seq2, 0, []byte("abc"), false)
	if ok, _ := db.Transition(seq2, []int{StateQueued, StateSending, StateWaiting}, StateCancelled, ""); !ok {
		t.Fatal("отмена")
	}
	if ok, _ := db.AddOutFragment(seq2, 0, []byte{1}); ok {
		t.Fatal("часть отменённого записана")
	}
	if err := db.PutChunk(seq2, 1, []byte("x"), false); !errors.Is(err, ErrStale) {
		t.Fatalf("кусок отменённого: %v", err)
	}
	if ch, _ := db.Chunks(seq2, -1, 16); len(ch) != 0 {
		t.Fatal("куски отменённого не удалены")
	}
	if _, ok, _ := db.NextOutgoing(c.ID); ok {
		t.Fatal("нечего отправлять")
	}
}

func TestReceiveBatch(t *testing.T) {
	db := open(t, tempPath(t))
	c := chat(1, 128)
	_ = db.UpsertChat(c)
	id := msgID(7)
	res, err := db.ReceiveBatch(c.ID, []Incoming{frag(c.ID, id, 2, 3), frag(c.ID, id, 0, 3), frag(c.ID, id, 0, 3), {Wire: []byte("мусор")}}, 5462)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Seq == 0 || res[0].Seq != res[1].Seq || res[2].Dropped || !res[3].Dropped || res[1].Completed {
		t.Fatalf("%+v", res)
	}
	seq := res[0].Seq
	m, _, _ := db.Message(seq)
	if m.ReceivedCount != 2 || m.State != StateReceiving || m.Total != 3 || m.Kind != KindPending || m.Size != -1 {
		t.Fatalf("повтор изменил счётчик: %+v", m)
	}
	res, _ = db.ReceiveBatch(c.ID, []Incoming{frag(c.ID, id, 1, 3)}, 5462)
	if !res[0].Completed {
		t.Fatal("все части")
	}
	if pend, _ := db.PendingDecrypt(); len(pend) != 1 || pend[0] != seq {
		t.Fatalf("ожидают расшифрования %v", pend)
	}
	frs, _ := db.InFragments(seq, -1, BatchRows)
	if len(frs) != 3 || frs[0].Idx != 0 || frs[2].Idx != 2 {
		t.Fatalf("%+v", frs)
	}
	// Несовпадение Total и превышение предела — CORRUPTED, фрагменты отброшены.
	other := msgID(8)
	r1, _ := db.ReceiveBatch(c.ID, []Incoming{frag(c.ID, other, 0, 2)}, 5462)
	r2, _ := db.ReceiveBatch(c.ID, []Incoming{frag(c.ID, other, 1, 4)}, 5462)
	if !r2[0].Dropped || !r2[0].Corrupted || r2[0].Seq != r1[0].Seq {
		t.Fatalf("%+v", r2)
	}
	m, _, _ = db.Message(r1[0].Seq)
	if m.State != StateCorrupted || m.ErrorCode != "MESSAGE_CORRUPTED" {
		t.Fatalf("%+v", m)
	}
	r3, _ := db.ReceiveBatch(c.ID, []Incoming{frag(c.ID, other, 0, 2)}, 5462)
	if !r3[0].Dropped {
		t.Fatal("часть сообщения в конечном состоянии записана")
	}
	big, _ := db.ReceiveBatch(c.ID, []Incoming{frag(c.ID, msgID(9), 0, 5463)}, 5462)
	if !big[0].Dropped || big[0].Seq != 0 {
		t.Fatalf("%+v", big)
	}
	foreign, _ := db.ReceiveBatch(c.ID, []Incoming{frag([16]byte{2}, msgID(10), 0, 1)}, 5462)
	if !foreign[0].Dropped {
		t.Fatal("часть чужого чата записана")
	}
	// Удаление сообщения удаляет его части и куски.
	if ok, _ := db.Transition(seq, []int{StateDecryptPending}, StateDecrypting, ""); !ok {
		t.Fatal("DECRYPTING")
	}
	w := db.ChunkWriterAt(seq)
	if _, err := w.WriteAt([]byte("x"), 1); err == nil {
		t.Fatal("некратное смещение принято")
	}
	if _, err := w.WriteAt(make([]byte, ChunkSize+1), 0); err == nil {
		t.Fatal("длинный кусок принят")
	}
	if _, err := w.WriteAt([]byte("hello"), ChunkSize); err != nil {
		t.Fatal(err)
	}
	if ch, _ := db.Chunks(seq, -1, 16); len(ch) != 1 || ch[0].Idx != 1 {
		t.Fatalf("%+v", ch)
	}
	if _, err := db.sql.Exec(`DELETE FROM messages WHERE seq = ?`, seq); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.sql.QueryRow(`SELECT (SELECT count(*) FROM in_fragments WHERE seq = ?) + (SELECT count(*) FROM message_chunks WHERE seq = ?)`, seq, seq).Scan(&n)
	if n != 0 {
		t.Fatal("части и куски не удалены вместе с сообщением")
	}
}

func TestFinishAndHistory(t *testing.T) {
	path := tempPath(t)
	db := open(t, path)
	c := chat(1, 128)
	_ = db.UpsertChat(c)
	res, _ := db.ReceiveBatch(c.ID, []Incoming{frag(c.ID, msgID(1), 0, 1)}, 5462)
	seq := res[0].Seq
	_, _ = db.Transition(seq, []int{StateDecryptPending}, StateDecrypting, "")
	if ok, err := db.FinishIncoming(seq, KindText, "текст", "", "", "", 10); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if frs, _ := db.InFragments(seq, -1, 16); len(frs) != 0 {
		t.Fatal("части после DONE")
	}
	out, _ := db.CreateOutgoing(Message{ChatID: c.ID, MessageID: msgID(2), Kind: KindText, Text: "ответ", Total: 1}, nil)
	ctl, _ := db.CreateOutgoing(Message{ChatID: c.ID, MessageID: msgID(3), Kind: KindControl, Total: 1, Size: 16, ChunksComplete: true}, make([]byte, 16))
	if next, _, _ := db.NextOutgoing(c.ID); next.Seq != ctl {
		t.Fatal("служебное должно быть впереди очереди")
	}
	unread, _ := db.UnreadCounts()
	if unread[c.ID] != 1 {
		t.Fatalf("непрочитанных %v", unread)
	}
	if n, _ := db.MarkRead(c.ID); n != 1 {
		t.Fatal("MarkRead")
	}
	if unread, _ = db.UnreadCounts(); unread[c.ID] != 0 {
		t.Fatal("после MarkRead")
	}
	list, more, _ := db.ListMessages(c.ID, 0, 1)
	if len(list) != 1 || !more || list[0].Seq != out {
		t.Fatalf("%+v %v", list, more)
	}
	list, more, _ = db.ListMessages(c.ID, out, 10)
	if len(list) != 1 || more || list[0].Seq != seq || list[0].Text != "текст" {
		t.Fatalf("%+v", list)
	}
	// Признак отмены отправителем до прихода частей.
	mseq, changed, err := db.CancelledByPeer(c.ID, msgID(5))
	if err != nil || !changed {
		t.Fatal(err)
	}
	r, _ := db.ReceiveBatch(c.ID, []Incoming{frag(c.ID, msgID(5), 0, 2)}, 5462)
	if !r[0].Dropped || r[0].Seq != mseq {
		t.Fatal("часть отменённого записана")
	}
	// Строка-признак не видна в истории и не считается в закрытых чатах.
	if list, _, _ := db.ListMessages(c.ID, 0, 10); len(list) != 2 {
		t.Fatalf("строка-признак видна в истории: %d", len(list))
	}
	if err := db.SetWanted(c.ID, true); err != nil {
		t.Fatal(err)
	}
	_ = db.MarkChatClosed(c.ID)
	closed, _ := db.ClosedChats()
	if len(closed) != 1 || closed[0].MessageCount != 2 {
		t.Fatalf("%+v", closed)
	}
	_ = db.Close()
	db = open(t, path)
	if w, ok, _ := db.Wanted(c.ID); !w || !ok {
		t.Fatal("желание принимать потеряно")
	}
	if err := db.DeleteChatHistory(c.ID); err != nil {
		t.Fatal(err)
	}
	if list, _, _ := db.ListMessages(c.ID, 0, 10); len(list) != 0 {
		t.Fatal("история не удалена")
	}
	if _, ok, _ := db.Chat(c.ID); !ok {
		t.Fatal("строка чата удалена")
	}
}

// Отмена, пришедшая после частей, остаётся видимой: отменённое отправителем сообщение показывается.
func TestPeerCancelAfterPartsVisible(t *testing.T) {
	db := open(t, tempPath(t))
	c := chat(1, 128)
	_ = db.UpsertChat(c)
	_, _ = db.ReceiveBatch(c.ID, []Incoming{frag(c.ID, msgID(7), 0, 3)}, 5462)
	if _, changed, err := db.CancelledByPeer(c.ID, msgID(7)); err != nil || !changed {
		t.Fatal(err)
	}
	list, _, _ := db.ListMessages(c.ID, 0, 10)
	if len(list) != 1 || list[0].State != StateCancelledByPeer {
		t.Fatalf("%+v", list)
	}
}

// После удаления открытого содержимого (отмена, сбой, повреждение, отмена отправителем, возврат
// прерванного расшифрования) его байтов нет ни в основном файле, ни в журнале WAL.
func TestWipedChunksLeaveNoBytes(t *testing.T) {
	for _, reopen := range []bool{false, true} {
		name := "запись только в журнале"
		if reopen {
			name = "запись уже в основном файле"
		}
		t.Run(name, func(t *testing.T) {
			path := tempPath(t)
			db := open(t, path)
			c := chat(1, 128)
			if err := db.UpsertChat(c); err != nil {
				t.Fatal(err)
			}
			type item struct {
				what string
				seq  int64
				data []byte
				wipe func(seq int64) error
			}
			var items []item
			// Исходящие файлы с копией содержимого: CANCELLED и FAILED.
			for i, to := range []int{StateCancelled, StateFailed} {
				seq, err := db.CreateOutgoing(Message{ChatID: c.ID, MessageID: msgID(byte(10 + i)), Kind: KindFile, FileName: "f",
					Size: 100, Total: 1, SourcePath: "x", SourceSize: 100, SourceMtime: 1}, nil)
				if err != nil {
					t.Fatal(err)
				}
				data := randomBytes(t, 200) // помещается в одну страницу: байты идут подряд
				if err := db.PutChunk(seq, 0, data, true); err != nil {
					t.Fatal(err)
				}
				to := to
				items = append(items, item{"исходящее", seq, data, func(seq int64) error {
					_, err := db.Transition(seq, []int{StateQueued}, to, "")
					return err
				}})
			}
			// Входящие в расшифровании: CORRUPTED, CANCELLED_BY_PEER, DECRYPT_CANCELLED и возврат после сбоя.
			incoming := func(id byte) (int64, []byte) {
				res, err := db.ReceiveBatch(c.ID, []Incoming{frag(c.ID, msgID(id), 0, 1)}, 5462)
				if err != nil {
					t.Fatal(err)
				}
				seq := res[0].Seq
				if ok, err := db.Transition(seq, []int{StateDecryptPending}, StateDecrypting, ""); !ok || err != nil {
					t.Fatal(ok, err)
				}
				data := randomBytes(t, 200) // помещается в одну страницу: байты идут подряд
				if err := db.PutChunk(seq, 0, data, false); err != nil {
					t.Fatal(err)
				}
				return seq, data
			}
			seq, data := incoming(20)
			items = append(items, item{"повреждённое", seq, data, func(seq int64) error {
				_, err := db.Transition(seq, []int{StateDecrypting}, StateCorrupted, "MESSAGE_CORRUPTED")
				return err
			}})
			seq, data = incoming(21)
			items = append(items, item{"отменённое расшифрование", seq, data, func(seq int64) error {
				_, err := db.Transition(seq, []int{StateDecrypting}, StateDecryptCancelled, "")
				return err
			}})
			seq, data = incoming(22)
			items = append(items, item{"отменённое отправителем", seq, data, func(int64) error {
				_, _, err := db.CancelledByPeer(c.ID, msgID(22))
				return err
			}})
			seq, data = incoming(23)
			items = append(items, item{"прерванное расшифрование", seq, data, func(int64) error { return db.ResetDecrypting() }})
			if reopen {
				_ = db.Close()
				db = open(t, path)
			}
			for _, it := range items {
				assertOnDisk(t, path, it.what, it.data)
				if err := it.wipe(it.seq); err != nil {
					t.Fatal(it.what, err)
				}
				if ch, _ := db.Chunks(it.seq, -1, 1); len(ch) != 0 {
					t.Fatalf("%s: куски остались", it.what)
				}
				assertNotOnDisk(t, path, it.what, it.data)
			}
			_ = db.Close()
			for _, it := range items {
				assertNotOnDisk(t, path, it.what+" после закрытия", it.data)
			}
		})
	}
}

// assertOnDisk проверяет, что значение есть в файлах базы: иначе проверка отсутствия ничего не доказывает.
func assertOnDisk(t *testing.T, path, what string, value []byte) {
	t.Helper()
	files, _ := filepath.Glob(path + "*")
	for _, f := range files {
		if raw, err := os.ReadFile(f); err == nil && bytes.Contains(raw, value) {
			return
		}
	}
	t.Fatalf("%s: байтов нет в файлах базы до удаления", what)
}
