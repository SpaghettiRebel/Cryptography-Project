package delivery

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"cryptochat/core/message"
)

var pngHead = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00")

func TestMetas(t *testing.T) {
	if m := TextMeta("привет"); m.Name != "" || m.Type != TextType || m.TotalSize != 12 {
		t.Fatalf("%+v", m)
	}
	if m := FileMeta(`C:\папка\отчёт.pdf`, 5); m.Name != "отчёт.pdf" || m.Type != "application/pdf" || m.TotalSize != 5 {
		t.Fatalf("%+v", m)
	}
	if m := FileMeta(`C:\a\file.нетакого`, 0); m.Type != "application/octet-stream" {
		t.Fatalf("%+v", m)
	}
	long := strings.Repeat("я", 600) + ".txt" // 1 204 байт
	m := FileMeta(long, 1)
	if len(m.Name) > message.MaxNameLen || !utf8.ValidString(m.Name) || m.Name == "" {
		t.Fatalf("длинное имя: %d байт", len(m.Name))
	}
	cm, data := CancelPayload([16]byte{1, 2, 3})
	if cm.Type != CancelType || cm.Name != "" || cm.TotalSize != 16 || !bytes.Equal(data, []byte{1, 2, 3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}) {
		t.Fatalf("%+v %x", cm, data)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		meta message.Meta
		head []byte
		size int64
		kind Kind
		file string
		mt   string
	}{
		{"текст", message.Meta{Type: TextType}, []byte("hi"), 2, KindText, "", ""},
		{"текст с другим регистром", message.Meta{Type: "Text/Plain ; Charset=UTF-8"}, []byte("hi"), 2, KindText, "", ""},
		{"длинный текст", message.Meta{Type: TextType}, []byte("hi"), MaxText + 1, KindFile, "сообщение.txt", ""},
		{"ровно 64 КиБ", message.Meta{Type: TextType}, []byte("hi"), MaxText, KindText, "", ""},
		{"отмена", message.Meta{Type: CancelType}, make([]byte, 16), 16, KindCancel, "", ""},
		{"отмена длиннее 16 байт", message.Meta{Type: CancelType}, make([]byte, 17), 17, KindFile, "файл", ""},
		{"отмена в мегабайт", message.Meta{Type: CancelType}, make([]byte, 512), 1 << 20, KindFile, "файл", ""},
		{"отмена короче 16 байт", message.Meta{Type: CancelType}, make([]byte, 15), 15, KindFile, "файл", ""},
		{"без имени", message.Meta{Type: "application/zip"}, []byte("PK"), 2, KindFile, "файл", ""},
		{"png", message.Meta{Name: "a.png", Type: "image/png"}, pngHead, 100, KindImage, "a.png", "image/png"},
		{"png под видом pdf", message.Meta{Name: "a.pdf", Type: "application/pdf"}, pngHead, 100, KindImage, "a.pdf", "image/png"},
		{"большой png", message.Meta{Name: "a.png", Type: "image/png"}, pngHead, MaxImage + 1, KindFile, "a.png", ""},
		{"svg", message.Meta{Name: "a.svg", Type: "image/svg+xml"}, []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>x</script></svg>`), 60, KindFile, "a.svg", ""},
		{"html как png", message.Meta{Name: "a.png", Type: "image/png"}, []byte("<html><script>alert(1)</script></html>"), 40, KindFile, "a.png", ""},
		{"пустой файл", message.Meta{Name: "empty", Type: "application/octet-stream"}, nil, 0, KindFile, "empty", ""},
		{"gif", message.Meta{Name: "g", Type: "x"}, []byte("GIF89a......"), 12, KindImage, "g", "image/gif"},
		{"jpeg", message.Meta{Name: "j", Type: "x"}, []byte("\xff\xd8\xff\xe0......"), 12, KindImage, "j", "image/jpeg"},
		{"bmp", message.Meta{Name: "b", Type: "x"}, []byte("BM......"), 8, KindImage, "b", "image/bmp"},
		{"webp", message.Meta{Name: "w", Type: "x"}, []byte("RIFF\x00\x00\x00\x00WEBPVP"), 14, KindImage, "w", "image/webp"},
	}
	for _, c := range cases {
		got := Classify(c.meta, c.head, c.size)
		if got.Kind != c.kind || got.Name != c.file || got.MediaType != c.mt {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestSafeFileName(t *testing.T) {
	cases := map[string]string{
		`..\..\evil.exe`:         "evil.exe",
		`../../etc/passwd`:       "passwd",
		"CON":                    "_CON",
		"con.txt":                "_con.txt",
		"LPT1":                   "_LPT1",
		"нормальное имя.pdf":     "нормальное имя.pdf",
		"a<b>c:d\"e|f?g*h":       "a_b_c_d_e_f_g_h",
		"ctrl\x01\x1f.txt":       "ctrl__.txt",
		"точки в конце...":       "точки в конце",
		"пробелы   ":             "пробелы",
		"":                       "файл",
		"..":                     "файл",
		`C:\`:                    "файл",
		strings.Repeat("ж", 300): strings.Repeat("ж", 255),
		"bad\xffutf8":            "bad_utf8",
		"COM9.tar.gz":            "_COM9.tar.gz",
		"CONSOLE":                "CONSOLE",
		// Смена направления письма и другие невидимые символы форматирования (категория Cf).
		"photo\u202Egpj.exe":         "photo_gpj.exe",
		"a\u200Eb\u200Fc":            "a_b_c",
		"x\u202A\u202B\u202C\u202Dy": "x____y",
		"\u2066имя\u2069.txt":        "_имя_.txt",
		"\u2067\u2068z":              "__z",
		"zero\u200Bwidth":            "zero_width",
		"bom\uFEFF.txt":              "bom_.txt",
		"soft\u00ADhyphen":           "soft_hyphen",
	}
	for in, want := range cases {
		if got := SafeFileName(in); got != want {
			t.Errorf("%q: %q, ожидалось %q", in, got, want)
		}
	}
}
