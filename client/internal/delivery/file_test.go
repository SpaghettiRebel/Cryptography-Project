//go:build devcipher

package delivery

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/localdb"
)

// sendNamed ставит файл в очередь с именем отправителя name (в обход filepath.Base).
func sendNamed(t *testing.T, s *side, chat [16]byte, name, typ string, data []byte) int64 {
	t.Helper()
	path := writeFile(t, data)
	st, _ := os.Stat(path)
	v, err := s.e.queue(localdb.Message{ChatID: chat, Kind: localdb.KindFile, FileName: name, FileType: typ,
		Size: st.Size(), Total: fragmentCount(st.Size()), SourcePath: path, SourceSize: st.Size(),
		SourceMtime: st.ModTime().UnixNano(), ChunksComplete: st.Size() == 0}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return v.Seq
}

// Файлы при всех 28 сочетаниях режима и набивки (в быстром прогоне — CBC с четырьмя набивками).
func TestFiles(t *testing.T) {
	modes := []int{1, 2, 3, 4, 5, 6, 7}
	if testing.Short() {
		modes = []int{2}
	}
	for i, combo := 0, 0; i < len(modes)*4; i++ {
		mode, pad := modes[i/4], i%4+1
		bits := []int{128, 192, 256}[combo%3]
		combo++
		t.Run("mode"+strconv.Itoa(mode)+"-pad"+strconv.Itoa(pad), func(t *testing.T) {
			p := newPair(t, mode, pad, bits)
			cap := &capture{}
			p.a.api.SetSendHook(cap.hook)
			p.a.receive()
			p.b.receive()
			files := map[string][]byte{
				"пустой-ИМЯМАРКЕР.txt": {},
				"один.bin":             {0},
				"некратный.bin":        randomBytes(localdb.ChunkSize + 1),
			}
			sent := map[int64]string{}
			for name, data := range files {
				sent[sendNamed(t, p.a, p.id, name, "application/octet-stream", data)] = name
			}
			got := p.b.waitIncoming(3, 10*time.Second, localdb.StateDone)
			for _, m := range got {
				want, ok := files[m.FileName]
				if !ok || m.Kind != localdb.KindFile || m.Size != int64(len(want)) || !bytes.Equal(p.b.content(m.Seq), want) {
					t.Fatalf("файл %q: вид %d размер %d", m.FileName, m.Kind, m.Size)
				}
			}
			for seq, name := range sent {
				m := p.a.waitState(seq, 5*time.Second, localdb.StateSent)
				if !m.ChunksComplete || !bytes.Equal(p.a.content(seq), files[name]) {
					t.Fatalf("копия файла у отправителя неполна: %q", name)
				}
			}
			for _, f := range cap.frags(t) {
				if bytes.Contains(f.Cipher, []byte("ИМЯМАРКЕР")) {
					t.Fatal("имя файла в шифротексте открытым текстом")
				}
			}
		})
	}
}

func TestImagesAndNames(t *testing.T) {
	p := newPair(t, 6, 3, 256)
	p.a.receive()
	p.b.receive()
	sendNamed(t, p.a, p.id, "картинка.png", "image/png", pngData)
	sendNamed(t, p.a, p.id, "a.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>1</script></svg>`))
	sendNamed(t, p.a, p.id, "html.png", "image/png", []byte("<html><script>alert(1)</script></html>"))
	sendNamed(t, p.a, p.id, `..\..\evil.exe`, "application/octet-stream", []byte("MZ"))
	sendNamed(t, p.a, p.id, "CON", "application/octet-stream", []byte("x"))
	want := map[string]int{"картинка.png": localdb.KindImage, "a.svg": localdb.KindFile, "html.png": localdb.KindFile,
		"evil.exe": localdb.KindFile, "_CON": localdb.KindFile}
	got := p.b.waitIncoming(5, 10*time.Second, localdb.StateDone)
	var image localdb.Message
	for _, m := range got {
		k, ok := want[m.FileName]
		if !ok || k != m.Kind {
			t.Fatalf("%q: вид %d", m.FileName, m.Kind)
		}
		if m.Kind == localdb.KindImage {
			image = m
		}
	}
	if image.MediaType != "image/png" {
		t.Fatalf("тип картинки %q", image.MediaType)
	}
	// Медиа-обработчик.
	get := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		p.b.e.ServeMedia(w, httptest.NewRequest(method, path, nil))
		return w
	}
	w := get("GET", MediaURL(image.Seq))
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("X-Content-Type-Options") != "nosniff" ||
		w.Header().Get("Cache-Control") != "no-store" || !bytes.Equal(w.Body.Bytes(), pngData) {
		t.Fatalf("картинка: %d %v", w.Code, w.Header())
	}
	for _, m := range got {
		if m.Kind == localdb.KindFile {
			if w := get("GET", MediaURL(m.Seq)); w.Code != http.StatusNotFound || w.Body.Len() != 0 {
				t.Fatalf("файл через обработчик: %d", w.Code)
			}
		}
	}
	for _, path := range []string{"/media/999", "/media/x", "/other/1"} {
		if w := get("GET", path); w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if w := get("POST", MediaURL(image.Seq)); w.Code != http.StatusNotFound {
		t.Fatalf("POST: %d", w.Code)
	}
	// У отправителя сообщение исходящее: обработчик его не отдаёт.
	if w := httptest.NewRecorder(); true {
		p.a.e.ServeMedia(w, httptest.NewRequest("GET", MediaURL(1), nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("исходящее: %d", w.Code)
		}
	}
	// Сохранение файла и картинки.
	dir := t.TempDir()
	out := filepath.Join(dir, "сохранено.png")
	if err := p.b.e.SaveFile(image.Seq, out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); !bytes.Equal(b, pngData) {
		t.Fatal("сохранённый файл отличается")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".cryptochat-*")); len(left) != 0 {
		t.Fatal("временный файл остался")
	}
	tv, _ := p.a.e.SendText(p.id, "текст")
	tm := p.b.waitIncoming(6, 5*time.Second, localdb.StateDone)
	for _, m := range tm {
		if m.Kind == localdb.KindText {
			if err := p.b.e.SaveFile(m.Seq, filepath.Join(dir, "t")); apperr.CodeOf(err) != apperr.NotAFile {
				t.Fatalf("текст как файл: %v", err)
			}
		}
	}
	_ = tv
	if err := p.b.e.SaveFile(image.Seq, filepath.Join(dir, "нет папки", "x")); apperr.CodeOf(err) != apperr.FileWrite {
		t.Fatalf("ошибка записи: %v", err)
	}
}

// Картинка больше 20 МиБ показывается как файл.
func TestLargeImageIsFile(t *testing.T) {
	if testing.Short() {
		t.Skip("передаёт 20 МиБ")
	}
	p := newPair(t, 6, 1, 128)
	p.a.receive()
	p.b.receive()
	data := append(append([]byte(nil), pngData...), make([]byte, MaxImage)...)
	sendNamed(t, p.a, p.id, "big.png", "image/png", data)
	got := p.b.waitIncoming(1, 60*time.Second, localdb.StateDone)
	if got[0].Kind != localdb.KindFile || got[0].MediaType != "" || got[0].Size != int64(len(data)) {
		t.Fatalf("%+v", got[0].Kind)
	}
}
