package delivery

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"cryptochat/client/internal/localdb"
	"cryptochat/core/protocol"
)

var pngData = append(append([]byte(nil), pngHead...), bytes.Repeat([]byte{0}, 200)...)

// Обработчик отдаёт только готовые картинки; незавершённое, файлы, чужие номера и другие методы — 404.
func TestMediaHandler(t *testing.T) {
	p := newPair(t, 2, 3, 128)
	db := p.b.db
	mk := func(b byte) int64 {
		f := &protocol.Fragment{ChatID: p.id, MessageID: [16]byte{b}, Total: 2, Cipher: []byte{1}}
		res, err := db.ReceiveBatch(p.id, []localdb.Incoming{{Wire: []byte{1}, Frag: f}}, MaxTotal)
		if err != nil {
			t.Fatal(err)
		}
		return res[0].Seq
	}
	image, file, pending := mk(1), mk(2), mk(3)
	for _, s := range []int64{image, file} {
		_, _ = db.Transition(s, []int{localdb.StateReceiving}, localdb.StateDecrypting, "")
		if err := db.PutChunk(s, 0, pngData, false); err != nil {
			t.Fatal(err)
		}
	}
	if ok, _ := db.FinishIncoming(image, localdb.KindImage, "", "a.png", "image/png", "image/png", int64(len(pngData))); !ok {
		t.Fatal("картинка")
	}
	if ok, _ := db.FinishIncoming(file, localdb.KindFile, "", "a.png", "image/png", "", int64(len(pngData))); !ok {
		t.Fatal("файл")
	}
	get := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		p.b.e.ServeMedia(w, httptest.NewRequest(method, path, nil))
		return w
	}
	w := get("GET", MediaURL(image))
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("X-Content-Type-Options") != "nosniff" ||
		w.Header().Get("Cache-Control") != "no-store" || !bytes.Equal(w.Body.Bytes(), pngData) {
		t.Fatalf("картинка: %d %v", w.Code, w.Header())
	}
	for _, c := range []struct{ method, path string }{{"GET", MediaURL(file)}, {"GET", MediaURL(pending)},
		{"GET", "/media/9999"}, {"GET", "/media/-1"}, {"GET", "/media/"}, {"POST", MediaURL(image)}, {"GET", "/x/1"}} {
		if w := get(c.method, c.path); w.Code != http.StatusNotFound || w.Body.Len() != 0 {
			t.Errorf("%s %s: %d", c.method, c.path, w.Code)
		}
	}
	v := p.b.e.View(p.b.msg(image))
	if v.MediaURL != MediaURL(image) || v.Kind != "image" {
		t.Fatalf("%+v", v)
	}
	if v := p.b.e.View(p.b.msg(file)); v.MediaURL != "" {
		t.Fatal("адрес у файла")
	}
}
