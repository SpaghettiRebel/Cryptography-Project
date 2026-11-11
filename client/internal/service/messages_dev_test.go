//go:build devcipher

package service

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/delivery"
	"cryptochat/client/internal/rpcclient"
	"cryptochat/core/cipher"
)

func blockOf(err error) string {
	if e, ok := apperr.As(err); ok && e.Code == apperr.SendBlocked {
		return e.Field
	}
	return "нет: " + apperr.CodeOf(err)
}

// Отправка недоступна, пока ключ не согласован, приём выключен или идёт на другом устройстве.
func TestSendBlocks(t *testing.T) {
	srv, a, b := pairWithContacts(t)
	id := serverChat(t, a, "bob", rpcclient.ChatParams{Algorithm: cipher.RC6, KeyBits: 128, Mode: 2, Padding: 3})
	hexID := hex.EncodeToString(id[:])
	eventually(t, 3*time.Second, "чат у alice", func() bool { _, ok := findChat(a, id); return ok })
	if _, err := a.svc.SendText(hexID, "рано"); blockOf(err) != apperr.BlockKeyNotReady {
		t.Fatalf("ключ не согласован: %v", err)
	}
	eventually(t, 3*time.Second, "чат у bob", func() bool { _, ok := findChat(b, id); return ok })
	if _, err := b.svc.ConnectChat(hexID); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "отправка доступна", func() bool { c, _ := findChat(a, id); return c.CanSend })
	if _, err := b.svc.DisconnectChat(hexID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.svc.SendText(hexID, "отключён"); blockOf(err) != apperr.BlockNotConnected {
		t.Fatalf("отключён: %v", err)
	}
	if c, _ := findChat(b, id); c.CanSend || c.SendBlock != apperr.BlockNotConnected || c.ReceiveState != delivery.RecvOff {
		t.Fatalf("снимок: %+v", c)
	}
	// Отключение переживает синхронизацию: приём сам не открывается.
	waitCycles(t, b, 3)
	if st, _ := b.svc.sess.engine.ReceiveState(id); st != delivery.RecvOff {
		t.Fatalf("приём открылся сам: %s", st)
	}
	if _, err := b.svc.ConnectChat(hexID); err != nil {
		t.Fatal(err)
	}
	eventually(t, 3*time.Second, "приём bob", func() bool { c, _ := findChat(b, id); return c.CanSend })
	srv.EndStream(id, "bob", apperr.New(apperr.StreamReplaced))
	eventually(t, 3*time.Second, "ELSEWHERE", func() bool { c, _ := findChat(b, id); return c.ReceiveState == delivery.RecvElsewhere })
	if _, err := b.svc.SendText(hexID, "там"); blockOf(err) != apperr.BlockReceivingElsewhere {
		t.Fatalf("на другом устройстве: %v", err)
	}
	if len(b.rec.notices(apperr.StreamReplaced)) != 1 {
		t.Fatal("нет уведомления STREAM_REPLACED")
	}
	if _, err := b.svc.ReceiveHere(hexID); err != nil {
		t.Fatal(err)
	}
	eventually(t, 3*time.Second, "приём здесь", func() bool { c, _ := findChat(b, id); return c.CanSend })
}

// История переживает перезапуск, новые входящие считаются до просмотра, исходящие не считаются,
// второй пользователь в той же папке данных сообщений первого не видит.
func TestHistoryAndUnread(t *testing.T) {
	srv, a, b, id, hexID := readyChat(t, 6, 4)
	for _, text := range []string{"один", "два"} {
		if _, err := a.svc.SendText(hexID, text); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.svc.SendText(hexID, "ответ"); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "два новых у bob", func() bool { c, _ := findChat(b, id); return c.Unread == 2 })
	if c, _ := findChat(a, id); c.Unread != 0 {
		eventually(t, 5*time.Second, "одно новое у alice", func() bool { c, _ := findChat(a, id); return c.Unread == 1 })
	}
	if err := b.svc.MarkRead(hexID); err != nil {
		t.Fatal(err)
	}
	if c, _ := findChat(b, id); c.Unread != 0 {
		t.Fatalf("после MarkRead: %d", c.Unread)
	}
	before, _ := b.svc.ListMessages(hexID, 0, 50)
	if len(before.Messages) != 3 {
		t.Fatalf("сообщений %d", len(before.Messages))
	}
	b.svc.Close()
	b2 := newParty(t, srv, b.root)
	if _, err := b2.svc.Login("bob", testPassword); err != nil {
		t.Fatal(err)
	}
	after, err := b2.svc.ListMessages(hexID, 0, 50)
	if err != nil || len(after.Messages) != 3 {
		t.Fatalf("после перезапуска: %v", err)
	}
	for i := range after.Messages {
		x, y := before.Messages[i], after.Messages[i]
		if x.Seq != y.Seq || x.State != y.State || x.Text != y.Text || x.Kind != y.Kind || x.Direction != y.Direction {
			t.Fatalf("история изменилась: %+v / %+v", x, y)
		}
	}
	// Другой пользователь в той же папке данных.
	b2.svc.Close()
	c := newParty(t, srv, b.root)
	if err := c.svc.Register("carol", testPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := c.svc.Login("carol", testPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := c.svc.ListMessages(hexID, 0, 50); apperr.CodeOf(err) != apperr.ChatNotFound {
		t.Fatalf("чужая история видна: %v", err)
	}
}

// Файл ровно 256 МиБ принимается (проверяется только предел: отправка сразу отменяется).
func TestFileLimitExact(t *testing.T) {
	_, a, _, _, hexID := readyChat(t, 2, 3)
	path := filepath.Join(t.TempDir(), "ровно.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Truncate(delivery.MaxFile)
	_ = f.Close()
	v, err := a.svc.SendFile(hexID, path)
	if err != nil {
		t.Fatalf("256 МиБ: %v", err)
	}
	if v.Size != delivery.MaxFile || v.FileName != "ровно.bin" {
		t.Fatalf("%+v", v)
	}
	if _, err := a.svc.CancelMessage(v.Seq); err != nil {
		t.Fatal(err)
	}
}
