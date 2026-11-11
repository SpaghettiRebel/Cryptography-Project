//go:build e2e && devcipher

package e2e

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cryptochat/client/internal/ciphers"
	"cryptochat/client/internal/service"
)

var (
	allModes    = []string{"ECB", "CBC", "PCBC", "CFB", "OFB", "CTR", "RANDOM_DELTA"}
	allPaddings = []string{"ZEROS", "ANSI_X923", "PKCS7", "ISO_10126"}
)

// История 1: тексты при всех 28 сочетаниях режима и набивки в обе стороны, граничные длины, повтор
// одного текста, отправка отключённому получателю, задержка доставки и отсутствие открытого текста и
// ключей на сервере и в брокере.
func TestTextMessaging(t *testing.T) {
	a, b := user(t, "txta"), user(t, "txtb")
	befriend(t, a, b)
	var reqs []service.CreateChatRequest
	for i, m := range allModes {
		for j, p := range allPaddings {
			bits := []int{128, 192, 256}[(i*len(allPaddings)+j)%3]
			reqs = append(reqs, service.CreateChatRequest{Algorithm: "RC6", KeyBits: bits, CipherMode: m, Padding: p})
		}
	}
	ids := readyChats(t, a, b, reqs)
	var markers []string
	for i, id := range ids {
		hexID := hex.EncodeToString(id[:])
		forward := fmt.Sprintf("ТЕКСТ-ВПЕРЁД-%02d-%s/%s\nс переводом строки  ", i, reqs[i].CipherMode, reqs[i].Padding)
		back := fmt.Sprintf("ТЕКСТ-НАЗАД-%02d", i)
		markers = append(markers, fmt.Sprintf("ТЕКСТ-ВПЕРЁД-%02d", i), back)
		if _, err := a.svc.SendText(hexID, forward); err != nil {
			t.Fatalf("%s/%s: %v", reqs[i].CipherMode, reqs[i].Padding, err)
		}
		if _, err := b.svc.SendText(hexID, back); err != nil {
			t.Fatal(err)
		}
		waitMessage(t, b, hexID, 30*time.Second, "текст у получателя "+reqs[i].CipherMode+"/"+reqs[i].Padding, incomingText(forward))
		waitMessage(t, a, hexID, 30*time.Second, "ответ у отправителя", incomingText(back))
	}
	t.Logf("SC-001: 28 сочетаний режима и набивки, тексты в обе стороны совпали")

	id, hexID := ids[9], hex.EncodeToString(ids[9][:])
	// Граничные длины.
	for _, n := range []int{1, 49153} {
		text := strings.Repeat("я", n/2) + strings.Repeat("z", n%2)
		if _, err := a.svc.SendText(hexID, text); err != nil {
			t.Fatal(err)
		}
		waitMessage(t, b, hexID, 30*time.Second, fmt.Sprintf("длина %d", n), incomingText(text))
	}
	// Один текст дважды — оба доставлены.
	for i := 0; i < 2; i++ {
		if _, err := a.svc.SendText(hexID, "ДВАЖДЫ-МЕТКА"); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, 20*time.Second, "оба одинаковых текста", func() bool {
		n := 0
		for _, m := range messages(t, b, hexID) {
			if m.Direction == "in" && m.Text == "ДВАЖДЫ-МЕТКА" && m.State == "DONE" {
				n++
			}
		}
		return n == 2
	})
	markers = append(markers, "ДВАЖДЫ-МЕТКА")

	// Задержка доставки у подключённого получателя.
	var worst time.Duration
	for i := 0; i < 5; i++ {
		text := fmt.Sprintf("ЗАДЕРЖКА-%d", i)
		start := time.Now()
		if _, err := a.svc.SendText(hexID, text); err != nil {
			t.Fatal(err)
		}
		waitMessage(t, b, hexID, 10*time.Second, "текст для замера", incomingText(text))
		if d := time.Since(start); d > worst {
			worst = d
		}
	}
	measure(t, "SC-002 текст SendText→DONE у подключённого получателя, худший из 5", worst.Round(time.Millisecond).String())
	if worst > 5*time.Second {
		t.Errorf("SC-002: доставка %v > 5 с", worst)
	}

	// Получатель отключён (приложение закрыто): отправка завершается без него, после входа текст доставлен.
	b.svc.Close()
	v, err := a.svc.SendText(hexID, "ПОКА-ВЫ-СПАЛИ")
	if err != nil {
		t.Fatal(err)
	}
	waitMessage(t, a, hexID, 20*time.Second, "SENT без получателя", bySeq(v.Seq, "SENT"))
	b.open()
	b.signIn()
	waitMessage(t, b, hexID, 30*time.Second, "текст после входа", incomingText("ПОКА-ВЫ-СПАЛИ"))
	markers = append(markers, "ПОКА-ВЫ-СПАЛИ")

	needles := append([]string{}, markers...)
	for _, m := range markers {
		needles = append(needles, hex.EncodeToString([]byte(m)))
	}
	needles = append(needles, sessionKeyNeedles(t, ids, a, b)...)
	searchNowhere(t, "метки текстов и сеансовые ключи", needles)
	_ = id
}

// История 2: картинка, пустой файл, один байт, некратная длина при Zeros и PKCS7, файл 50 МиБ с замером.
func TestFileMessaging(t *testing.T) {
	a, b := user(t, "fila"), user(t, "filb")
	befriend(t, a, b)
	var names []string
	for _, pad := range []string{"ZEROS", "PKCS7"} {
		_, hexID := readyChat(t, a, b, "CBC", pad, 256)
		dir := t.TempDir()
		files := map[string][]byte{
			"пустой-" + pad + ".bin":        {},
			"один-" + pad + ".bin":          {0},
			"некратный-ИМЯ-" + pad + ".bin": bytes.Repeat([]byte("НЕКРАТНЫЙ-МАРКЕР"), 3333),
		}
		for name, data := range files {
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			sendFile(t, a, hexID, path)
			m, _ := waitMessage(t, b, hexID, 30*time.Second, "файл "+name, incomingFile(name, "DONE"))
			if m.Kind != "file" || m.Size != int64(len(data)) || !savedEqual(t, b, m.Seq, path) {
				t.Fatalf("%s: вид %s размер %d", name, m.Kind, m.Size)
			}
			names = append(names, name)
		}
	}
	_, hexID := readyChat(t, a, b, "CTR", "ISO_10126", 128)
	png := filepath.Join(t.TempDir(), "картинка.png")
	pngData := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00"), bytes.Repeat([]byte{7}, 500)...)
	if err := os.WriteFile(png, pngData, 0o600); err != nil {
		t.Fatal(err)
	}
	sendFile(t, a, hexID, png)
	img, _ := waitMessage(t, b, hexID, 30*time.Second, "картинка", incomingFile("картинка.png", "DONE"))
	if img.Kind != "image" || img.MediaURL == "" || !savedEqual(t, b, img.Seq, png) {
		t.Fatalf("картинка: %+v", img.Kind)
	}

	const marker = "БОЛЬШОЙ-ФАЙЛ-МЕТКА-50"
	big := markedFile(t, "большой-ИМЯ-50.bin", 50<<20, marker)
	start := time.Now()
	sendFile(t, a, hexID, big)
	m, _ := waitMessage(t, b, hexID, 6*time.Minute, "файл 50 МиБ", incomingFile("большой-ИМЯ-50.bin", "DONE"))
	d := time.Since(start)
	measure(t, "SC-003 файл 50 МиБ SendFile→DONE у получателя (CTR, ISO 10126)", d.Round(time.Second).String())
	if d > 3*time.Minute {
		t.Errorf("SC-003: %v > 3 мин", d)
	}
	if !savedEqual(t, b, m.Seq, big) {
		t.Fatal("файл 50 МиБ отличается")
	}
	needles := []string{marker, hex.EncodeToString([]byte(marker)), "НЕКРАТНЫЙ-МАРКЕР", "большой-ИМЯ-50", "некратный-ИМЯ"}
	for _, n := range names {
		needles = append(needles, n)
	}
	searchNowhere(t, "метки содержимого и имена файлов", needles)
}

// История 5: история переживает перезапуск обоих приложений; закрытый чат виден в списке закрытых и его
// история удаляется.
func TestHistoryAfterRestart(t *testing.T) {
	a, b := user(t, "hisa"), user(t, "hisb")
	befriend(t, a, b)
	_, hexID := readyChat(t, a, b, "OFB", "ANSI_X923", 192)
	if _, err := a.svc.SendText(hexID, "история-текст"); err != nil {
		t.Fatal(err)
	}
	png := filepath.Join(t.TempDir(), "фото.png")
	_ = os.WriteFile(png, append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{1}, 300)...), 0o600)
	sendFile(t, a, hexID, png)
	doc := markedFile(t, "документ.bin", 300_000, "ИСТОРИЯ")
	sendFile(t, b, hexID, doc)
	waitMessage(t, b, hexID, 30*time.Second, "текст", incomingText("история-текст"))
	waitMessage(t, b, hexID, 30*time.Second, "картинка", incomingFile("фото.png", "DONE"))
	waitMessage(t, a, hexID, 30*time.Second, "документ", incomingFile("документ.bin", "DONE"))
	eventually(t, 20*time.Second, "исходящие отправлены", func() bool {
		for _, p := range []*participant{a, b} {
			for _, m := range messages(t, p, hexID) {
				if m.Direction == "out" && m.State != "SENT" {
					return false
				}
			}
		}
		return true
	})
	before := map[*participant][]service.MessageView{a: messages(t, a, hexID), b: messages(t, b, hexID)}
	for _, p := range []*participant{a, b} {
		p.restart()
		p.signIn()
	}
	for _, p := range []*participant{a, b} {
		after := messages(t, p, hexID)
		if len(after) != len(before[p]) {
			t.Fatalf("%s: сообщений %d, было %d", p.login, len(after), len(before[p]))
		}
		for i, m := range after {
			x := before[p][i]
			if m.Seq != x.Seq || m.Kind != x.Kind || m.State != x.State || m.Text != x.Text || m.FileName != x.FileName || m.Size != x.Size {
				t.Fatalf("%s: сообщение %d изменилось", p.login, m.Seq)
			}
		}
	}
	for _, m := range messages(t, a, hexID) {
		if m.Direction == "in" && m.FileName == "документ.bin" && !savedEqual(t, a, m.Seq, doc) {
			t.Fatal("файл из истории отличается")
		}
	}
	t.Log("SC-009: история обоих совпала после перезапуска, файл из истории сохранён побайтно равным")
	if err := a.svc.CloseChat(hexID); err != nil {
		t.Fatal(err)
	}
	closed, err := a.svc.ListClosedChats()
	if err != nil || len(closed) != 1 || closed[0].MessageCount != 3 {
		t.Fatalf("закрытые чаты: %+v %v", closed, err)
	}
	if err := a.svc.DeleteChatHistory(hexID); err != nil {
		t.Fatal(err)
	}
	if len(messages(t, a, hexID)) != 0 {
		t.Fatal("история не удалена")
	}
	if !ciphers.DevBuild {
		t.Fatal("сборка без тестового шифра")
	}
}
