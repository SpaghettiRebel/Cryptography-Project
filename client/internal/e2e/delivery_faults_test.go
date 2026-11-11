//go:build e2e && devcipher

package e2e

import (
	"encoding/hex"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/delivery"
	"cryptochat/client/internal/service"
)

// История 3: остановка сервера во время передачи файла 20 МиБ — передача и приём возобновляются сами.
func TestServerRestartDuringTransfer(t *testing.T) {
	a, b := user(t, "srva"), user(t, "srvb")
	befriend(t, a, b)
	_, hexID := readyChat(t, a, b, "CBC", "PKCS7", 256)
	src := markedFile(t, "двадцать.bin", 20<<20, "СБОЙ-СЕРВЕРА")
	sendFile(t, a, hexID, src)
	eventually(t, 2*time.Minute, "приём начался", func() bool {
		m, ok := lastIncoming(t, b, hexID)
		return ok && m.Receive != nil && m.Receive.Done > 0
	})
	stopServer(t)
	m, _ := lastIncoming(t, b, hexID)
	var before int64
	if m.Receive != nil {
		before = m.Receive.Done
	}
	startServer(t)
	ready := time.Now()
	eventually(t, time.Minute, "новая часть после возврата сервера", func() bool {
		m, ok := lastIncoming(t, b, hexID)
		return ok && (m.State != "RECEIVING" || (m.Receive != nil && m.Receive.Done > before))
	})
	d := time.Since(ready)
	measure(t, "SC-005 готовность сервера → первая новая принятая часть", d.Round(time.Millisecond).String())
	if d > 10*time.Second {
		t.Errorf("SC-005: %v > 10 с", d)
	}
	got, _ := waitMessage(t, b, hexID, 5*time.Minute, "файл после сбоя", incomingFile("двадцать.bin", "DONE"))
	if !savedEqual(t, b, got.Seq, src) {
		t.Fatal("файл отличается")
	}
}

// Перезапуск приложения отправителя посреди передачи и получателя посреди приёма: одно целое сообщение.
func TestClientRestarts(t *testing.T) {
	a, b := user(t, "rsta"), user(t, "rstb")
	befriend(t, a, b)
	_, hexID := readyChat(t, a, b, "PCBC", "ANSI_X923", 128)
	src := markedFile(t, "перезапуски.bin", 15<<20, "ПЕРЕЗАПУСК")
	v := sendFile(t, a, hexID, src)
	eventually(t, time.Minute, "часть отправлена", func() bool {
		for _, m := range messages(t, a, hexID) {
			if m.Seq == v.Seq && m.Send != nil && m.Send.Done > 5 {
				return true
			}
		}
		return false
	})
	a.restart()
	a.signIn()
	eventually(t, 2*time.Minute, "приём начался", func() bool {
		m, ok := lastIncoming(t, b, hexID)
		return ok && m.Receive != nil && m.Receive.Done > 20
	})
	b.restart()
	b.signIn()
	got, _ := waitMessage(t, b, hexID, 5*time.Minute, "файл", incomingFile("перезапуски.bin", "DONE"))
	n := 0
	for _, m := range messages(t, b, hexID) {
		if m.Direction == "in" {
			n++
		}
	}
	if n != 1 || !savedEqual(t, b, got.Seq, src) {
		t.Fatalf("SC-004: входящих %d", n)
	}
	t.Log("SC-004: после перезапусков отправителя и получателя одно целое сообщение")
}

// Отключённый получатель: файл 70 МиБ упирается в предел очереди (WAITING/QUEUE_FULL), в данных брокера
// нет открытого содержимого и имени; после входа получателя передача завершается.
func TestQueueFullOfflineReceiver(t *testing.T) {
	a, b := user(t, "qfa"), user(t, "qfb")
	befriend(t, a, b)
	_, hexID := readyChat(t, a, b, "CFB", "PKCS7", 256)
	b.svc.Close()
	const marker = "ОЧЕРЕДЬ-МЕТКА-70"
	src := markedFile(t, "семьдесят-ИМЯ.bin", 70<<20, marker)
	v := sendFile(t, a, hexID, src)
	waitMessage(t, a, hexID, 5*time.Minute, "WAITING/QUEUE_FULL", func(m service.MessageView) bool {
		return m.Seq == v.Seq && m.State == "WAITING" && m.Error != nil && m.Error.Code == apperr.QueueFull
	})
	queues := brokerQueues(t)
	t.Logf("очереди брокера при заполнении: %s", strings.TrimSpace(queues))
	if len(a.rec.notices(apperr.QueueFull)) != 1 {
		t.Errorf("уведомлений QUEUE_FULL: %d", len(a.rec.notices(apperr.QueueFull)))
	}
	searchNowhere(t, "метка и имя файла в очереди", []string{marker, hex.EncodeToString([]byte(marker)), "семьдесят-ИМЯ"})
	b.open()
	b.signIn()
	got, _ := waitMessage(t, b, hexID, 10*time.Minute, "файл 70 МиБ", incomingFile("семьдесят-ИМЯ.bin", "DONE"))
	waitMessage(t, a, hexID, time.Minute, "SENT", bySeq(v.Seq, "SENT"))
	if !savedEqual(t, b, got.Seq, src) {
		t.Fatal("файл 70 МиБ отличается")
	}
}

// Третья копия того же пользователя с другой папкой данных забирает приём: у первой ELSEWHERE и
// уведомление, сама она поток не переоткрывает.
func TestReceiveElsewhere(t *testing.T) {
	a, b := user(t, "elsa"), user(t, "elsb")
	befriend(t, a, b)
	id, hexID := readyChat(t, a, b, "ECB", "PKCS7", 128)
	third := &participant{t: t, login: b.login, password: b.password, home: t.TempDir()}
	if err := saveSettings(third.home); err != nil {
		t.Fatal(err)
	}
	third.open()
	third.signIn()
	eventually(t, 30*time.Second, "чат у третьей копии", func() bool { _, ok := third.chat(id); return ok })
	if _, err := third.svc.ReceiveHere(hexID); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "ELSEWHERE у первой", func() bool {
		c, ok := b.chat(id)
		return ok && c.ReceiveState == delivery.RecvElsewhere && !c.CanSend && c.SendBlock == apperr.BlockReceivingElsewhere
	})
	if len(b.rec.notices(apperr.StreamReplaced)) == 0 {
		t.Fatal("нет уведомления STREAM_REPLACED")
	}
	opens := b.spy.openCalls()
	time.Sleep(15 * time.Second)
	if n := b.spy.openCalls(); n != opens {
		t.Fatalf("FR-013: первая копия открыла поток сама %d раз", n-opens)
	}
	if c, _ := b.chat(id); c.ReceiveState != delivery.RecvElsewhere {
		t.Fatalf("состояние %s", c.ReceiveState)
	}
	if _, err := a.svc.SendText(hexID, "на третью копию"); err != nil {
		t.Fatal(err)
	}
	// Ключа на новом устройстве нет (секрет остался на первом), поэтому третья копия только принимает
	// части; у первой копии сообщения нет.
	waitMessage(t, third, hexID, 30*time.Second, "сообщение у третьей копии", func(m service.MessageView) bool { return m.Direction == "in" })
	if _, ok := lastIncoming(t, b, hexID); ok {
		t.Fatal("первая копия приняла сообщение, хотя приём на другом устройстве")
	}
}

// Файл 256 МиБ между двумя сервисами одного процесса: прирост HeapInuse не больше 128 МиБ.
func TestMemory256(t *testing.T) {
	a, b := user(t, "mema"), user(t, "memb")
	befriend(t, a, b)
	_, hexID := readyChat(t, a, b, "CTR", "ZEROS", 256)
	src := markedFile(t, "предел.bin", 256<<20, "ПАМЯТЬ")
	h := startHeapSampler()
	start := time.Now()
	sendFile(t, a, hexID, src)
	got, _ := waitMessage(t, b, hexID, 20*time.Minute, "файл 256 МиБ", incomingFile("предел.bin", "DONE"))
	growth := h.growth()
	measure(t, "SC-008 прирост HeapInuse процесса теста при передаче 256 МиБ (оба сервиса)", mib(growth))
	measure(t, "время передачи 256 МиБ SendFile→DONE", time.Since(start).Round(time.Second).String())
	if growth > 128<<20 {
		t.Errorf("SC-008: прирост %s > 128 МиБ", mib(growth))
	}
	if !savedEqual(t, b, got.Seq, src) {
		t.Fatal("файл 256 МиБ отличается")
	}
}

// История 4: отмена отправителем, отмена приёма, отмена расшифрования и повтор, отключение посреди приёма.
func TestCancelOperations(t *testing.T) {
	a, b := user(t, "cana"), user(t, "canb")
	befriend(t, a, b)
	_, hexID := readyChat(t, a, b, "CBC", "PKCS7", 256)

	// Отмена отправителем во время передачи.
	src := markedFile(t, "отмена-отправителем.bin", 30<<20, "ОТМЕНА")
	v := sendFile(t, a, hexID, src)
	eventually(t, time.Minute, "первые части отправлены", func() bool {
		for _, m := range messages(t, a, hexID) {
			if m.Seq == v.Seq && m.Send != nil && m.Send.Done > 3 {
				return true
			}
		}
		return false
	})
	start := time.Now()
	cv, err := a.svc.CancelMessage(v.Seq)
	if err != nil || cv.State != "CANCELLED" {
		t.Fatalf("%+v %v", cv.State, err)
	}
	measure(t, "SC-006 отмена передачи отправителем", time.Since(start).Round(time.Millisecond).String())
	if time.Since(start) > 2*time.Second {
		t.Error("отмена дольше 2 с")
	}
	waitMessage(t, b, hexID, time.Minute, "CANCELLED_BY_PEER", func(m service.MessageView) bool {
		return m.Direction == "in" && m.State == "CANCELLED_BY_PEER"
	})

	// Отмена приёма получателем: следующий текст доставлен.
	src2 := markedFile(t, "отмена-приёма.bin", 30<<20, "ПРИЁМ")
	sendFile(t, a, hexID, src2)
	var recv service.MessageView
	eventually(t, time.Minute, "приём идёт", func() bool {
		m, ok := lastIncoming(t, b, hexID)
		recv = m
		return ok && m.State == "RECEIVING" && m.Receive != nil && m.Receive.Done > 3
	})
	start = time.Now()
	if cv, err := b.svc.CancelMessage(recv.Seq); err != nil || cv.State != "CANCELLED" {
		t.Fatalf("отмена приёма: %v", err)
	}
	measure(t, "SC-006 отмена приёма", time.Since(start).Round(time.Millisecond).String())
	if _, err := a.svc.SendText(hexID, "после отмены приёма"); err != nil {
		t.Fatal(err)
	}
	waitMessage(t, b, hexID, 5*time.Minute, "текст после отмены приёма", incomingText("после отмены приёма"))

	// Отмена расшифрования и повтор: отмена вызывается сразу по событию о начале расшифрования.
	type cancelled struct {
		seq   int64
		state string
		err   error
		took  time.Duration
	}
	res := make(chan cancelled, 1)
	var armed atomic.Bool
	armed.Store(true)
	b.svc.Subscribe(service.EmitterFunc(func(event string, data any) {
		ev, ok := data.(service.MessagesEvent)
		if !ok {
			return
		}
		for _, m := range ev.Changed {
			if m.Direction == "in" && (m.State == "DECRYPTING" || m.State == "DECRYPT_PENDING") && armed.CompareAndSwap(true, false) {
				seq := m.Seq
				go func() {
					start := time.Now()
					v, err := b.svc.CancelMessage(seq)
					res <- cancelled{seq, v.State, err, time.Since(start)}
				}()
			}
		}
	}))
	src3 := markedFile(t, "расшифрование.bin", 60<<20, "РАСШИФРОВАНИЕ")
	sendFile(t, a, hexID, src3)
	var dec cancelled
	select {
	case dec = <-res:
	case <-time.After(5 * time.Minute):
		t.Fatal("расшифрование не началось")
	}
	if dec.err != nil || dec.state != "DECRYPT_CANCELLED" {
		t.Fatalf("отмена расшифрования: %s %v", dec.state, dec.err)
	}
	measure(t, "SC-006 отмена расшифрования", dec.took.Round(time.Millisecond).String())
	if _, err := b.svc.RetryDecrypt(dec.seq); err != nil {
		t.Fatal(err)
	}
	done, _ := waitMessage(t, b, hexID, 5*time.Minute, "DONE после повтора", bySeq(dec.seq, "DONE"))
	if !savedEqual(t, b, done.Seq, src3) {
		t.Fatal("файл после повтора расшифрования отличается")
	}

	// Отключение получателя посреди приёма и подключение: файл целый (FR-029).
	src4 := markedFile(t, "отключение.bin", 20<<20, "ОТКЛЮЧЕНИЕ")
	sendFile(t, a, hexID, src4)
	eventually(t, time.Minute, "приём четвёртого файла", func() bool {
		m, ok := lastIncoming(t, b, hexID)
		return ok && m.FileName == "" && m.State == "RECEIVING" && m.Receive != nil && m.Receive.Done > 3
	})
	if _, err := b.svc.DisconnectChat(hexID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if _, err := b.svc.ConnectChat(hexID); err != nil {
		t.Fatal(err)
	}
	got, _ := waitMessage(t, b, hexID, 5*time.Minute, "файл после переподключения", incomingFile("отключение.bin", "DONE"))
	if !savedEqual(t, b, got.Seq, src4) {
		t.Fatal("файл после переподключения отличается")
	}
}
