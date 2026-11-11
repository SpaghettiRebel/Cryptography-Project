//go:build e2e && devcipher

package e2e

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cryptochat/client/internal/keyagree"
	"cryptochat/client/internal/profile"
	"cryptochat/client/internal/service"
)

// measure выводит строку замера; отчёт прогона собирает их в конец.
func measure(t *testing.T, what string, value string) {
	t.Helper()
	line := fmt.Sprintf("ЗАМЕР: %s %s", what, value)
	fmt.Println(line)
	t.Log(line)
}

// chatOf — чат участника из снимка.
func chatOf(p *participant, id [16]byte) (service.ChatInfo, bool) { return p.chat(id) }

// readyChats создаёт чаты a с b с заданными параметрами, подключает b и ждёт, пока у обоих ключ
// согласован и отправка доступна.
func readyChats(t *testing.T, a, b *participant, params []service.CreateChatRequest) [][16]byte {
	t.Helper()
	var ids [][16]byte
	for _, req := range params {
		req.Peer = b.login
		c, err := a.svc.CreateChat(req)
		if err != nil {
			t.Fatalf("чат %+v: %v", req, err)
		}
		ids = append(ids, c.ID)
	}
	eventually(t, 30*time.Second, "чаты у получателя", func() bool {
		for _, id := range ids {
			if _, ok := b.chat(id); !ok {
				return false
			}
		}
		return true
	})
	for _, id := range ids {
		connect(t, b, id)
	}
	eventually(t, 2*time.Minute, "ключи и приём у обоих", func() bool {
		for _, id := range ids {
			for _, p := range []*participant{a, b} {
				c, ok := p.chat(id)
				if !ok || c.KeyState != keyagree.Ready || !c.CanSend {
					return false
				}
			}
		}
		return true
	})
	return ids
}

func readyChat(t *testing.T, a, b *participant, mode, pad string, bits int) ([16]byte, string) {
	t.Helper()
	id := readyChats(t, a, b, []service.CreateChatRequest{{Algorithm: "RC6", KeyBits: bits, CipherMode: mode, Padding: pad}})[0]
	return id, hex.EncodeToString(id[:])
}

// messages — история чата участника.
func messages(t *testing.T, p *participant, hexID string) []service.MessageView {
	t.Helper()
	page, err := p.svc.ListMessages(hexID, 0, 200)
	if err != nil {
		t.Fatalf("история %s: %v", p.login, err)
	}
	return page.Messages
}

// waitMessage ждёт сообщение, удовлетворяющее условию, и возвращает его и время ожидания.
func waitMessage(t *testing.T, p *participant, hexID string, limit time.Duration, what string, cond func(service.MessageView) bool) (service.MessageView, time.Duration) {
	t.Helper()
	var found service.MessageView
	d := eventually(t, limit, what, func() bool {
		for _, m := range messages(t, p, hexID) {
			if cond(m) {
				found = m
				return true
			}
		}
		return false
	})
	return found, d
}

func bySeq(seq int64, state string) func(service.MessageView) bool {
	return func(m service.MessageView) bool { return m.Seq == seq && m.State == state }
}

func incomingText(text string) func(service.MessageView) bool {
	return func(m service.MessageView) bool { return m.Direction == "in" && m.State == "DONE" && m.Text == text }
}

func incomingFile(name string, state string) func(service.MessageView) bool {
	return func(m service.MessageView) bool { return m.Direction == "in" && m.State == state && m.FileName == name }
}

// lastIncoming — последнее входящее сообщение.
func lastIncoming(t *testing.T, p *participant, hexID string) (service.MessageView, bool) {
	t.Helper()
	list := messages(t, p, hexID)
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Direction == "in" {
			return list[i], true
		}
	}
	return service.MessageView{}, false
}

// markedFile создаёт файл размера size со случайным содержимым и меткой в начале, середине и конце.
func markedFile(t *testing.T, name string, size int64, marker string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, 1<<20)
	for written := int64(0); written < size; {
		n := int64(len(buf))
		if size-written < n {
			n = size - written
		}
		_, _ = rand.Read(buf[:n])
		if _, err := f.Write(buf[:n]); err != nil {
			t.Fatal(err)
		}
		written += n
	}
	if int64(len(marker)) <= size {
		for _, off := range []int64{0, size / 2, size - int64(len(marker))} {
			if _, err := f.WriteAt([]byte(marker), off); err != nil {
				t.Fatal(err)
			}
		}
	}
	return path
}

// sameFile сравнивает файлы побайтно, не читая их целиком.
func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	fa, err := os.Open(a)
	if err != nil {
		t.Fatal(err)
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	defer fb.Close()
	ba, bb := make([]byte, 1<<20), make([]byte, 1<<20)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false
		}
		if ea != nil || eb != nil {
			return (ea == io.EOF || ea == io.ErrUnexpectedEOF) && (eb == io.EOF || eb == io.ErrUnexpectedEOF)
		}
	}
}

// savedEqual сохраняет файл сообщения сервисом и сравнивает с исходным.
func savedEqual(t *testing.T, p *participant, seq int64, src string) bool {
	t.Helper()
	out := filepath.Join(t.TempDir(), "сохранено.bin")
	if err := p.svc.SaveFile(seq, out); err != nil {
		t.Fatalf("сохранение: %v", err)
	}
	return sameFile(t, out, src)
}

// sendFile отправляет файл и возвращает исходящее сообщение.
func sendFile(t *testing.T, p *participant, hexID, path string) service.MessageView {
	t.Helper()
	v, err := p.svc.SendFile(hexID, path)
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	return v
}

func rabbit(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runCompose(append([]string{"exec", "-T", "rabbitmq"}, args...)...)
}

// brokerQueues — очереди брокера с числом сообщений.
func brokerQueues(t *testing.T) string {
	t.Helper()
	out, _ := rabbit(t, "rabbitmqctl", "-q", "list_queues", "name", "messages", "--no-table-headers")
	return out
}

// sessionKeyNeedles — сеансовые ключи участников в hex и base64.
func sessionKeyNeedles(t *testing.T, ids [][16]byte, ps ...*participant) []string {
	t.Helper()
	var out []string
	for _, p := range ps {
		db := p.store()
		if db == nil {
			continue
		}
		for _, id := range ids {
			if k, ok, _ := db.SessionKey(id); ok {
				out = append(out, hex.EncodeToString(k), base64.StdEncoding.EncodeToString(k))
			}
		}
	}
	return out
}

// searchNowhere проверяет, что строки не найдены в журналах сервера, дампе базы и файлах брокера.
func searchNowhere(t *testing.T, what string, needles []string) {
	t.Helper()
	files, err := rabbit(t, "find", "/var/lib/rabbitmq", "-type", "f")
	if err != nil || !strings.Contains(files, "/var/lib/rabbitmq/") {
		t.Fatalf("в данных брокера нет файлов: поиск ничего не значит (%v)", err)
	}
	logs := serverLogs(t)
	dump := pgDump(t)
	if !strings.Contains(dump, "chat_participants") {
		t.Fatal("в дампе нет чатов: поиск ничего не значит")
	}
	for _, n := range needles {
		if n == "" {
			continue
		}
		if strings.Contains(logs, n) {
			t.Errorf("SC-007: %s в журнале сервера", what)
		}
		if strings.Contains(dump, n) {
			t.Errorf("SC-007: %s в базе сервера", what)
		}
		// -s: файлы, исчезающие во время поиска (журналы брокера), не считаются находкой.
		out, err := rabbit(t, "grep", "-rlFs", "-e", n, "/var/lib/rabbitmq")
		if err == nil || strings.TrimSpace(out) != "" {
			t.Errorf("SC-007: %s в файлах брокера: %s", what, out)
		}
	}
	t.Logf("SC-007: %s (%d строк поиска) не найдены в журналах сервера, pg_dump и файлах брокера", what, len(needles))
}

// heapSampler замеряет наибольший HeapInuse процесса теста в фоне.
type heapSampler struct {
	base int64
	max  atomic.Int64
	stop chan struct{}
	wg   sync.WaitGroup
}

func startHeapSampler() *heapSampler {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	h := &heapSampler{base: int64(ms.HeapInuse), stop: make(chan struct{})}
	h.max.Store(h.base)
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-h.stop:
				return
			case <-t.C:
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				if v := int64(ms.HeapInuse); v > h.max.Load() {
					h.max.Store(v)
				}
			}
		}
	}()
	return h
}

// growth останавливает замер и возвращает прирост HeapInuse над начальным значением.
func (h *heapSampler) growth() int64 {
	close(h.stop)
	h.wg.Wait()
	return h.max.Load() - h.base
}

func mib(n int64) string { return fmt.Sprintf("%.1f МиБ", float64(n)/(1<<20)) }

// saveSettings записывает настройки сервера в папку данных новой копии приложения.
func saveSettings(home string) error {
	return profile.Save(home, profile.Settings{ServerAddress: address, CertificatePath: certFile})
}
