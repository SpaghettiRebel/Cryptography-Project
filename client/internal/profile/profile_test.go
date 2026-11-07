package profile

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"cryptochat/client/internal/apperr"
)

func TestRoot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CRYPTOCHAT_HOME", dir)
	r, err := Root()
	if err != nil || r != dir {
		t.Fatalf("%q %v", r, err)
	}
	t.Setenv("CRYPTOCHAT_HOME", "")
	r, err = Root()
	if err != nil || filepath.Base(r) != "cryptochat" {
		t.Fatalf("%q %v", r, err)
	}
}

func TestDefaultsWhenMissing(t *testing.T) {
	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != 1 || s.ServerAddress != "localhost:8443" || len(s.RecentLogins) != 0 {
		t.Fatalf("%+v", s)
	}
}

func TestDefaultCertificateFromWorkingDir(t *testing.T) {
	wd, _ := os.Getwd()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "deploy", "certs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deploy", "certs", "server.crt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()
	s := Defaults()
	if !filepath.IsAbs(s.CertificatePath) || !strings.HasSuffix(filepath.ToSlash(s.CertificatePath), "deploy/certs/server.crt") {
		t.Fatalf("%q", s.CertificatePath)
	}
}

func TestSaveLoadRoundTripWithCyrillicPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "папка с пробелом")
	s := Defaults()
	s.ServerAddress = "127.0.0.1:18445"
	s.CertificatePath = `C:\сертификаты\server.crt`
	s.AddRecentLogin("alice")
	if err := Save(root, s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerAddress != s.ServerAddress || got.CertificatePath != s.CertificatePath || len(got.RecentLogins) != 1 {
		t.Fatalf("%+v", got)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 || entries[0].Name() != "settings.json" {
		t.Fatalf("лишние файлы после атомарной записи: %v", entries)
	}
}

func TestCorruptedSettings(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte("{не json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(root)
	if apperr.CodeOf(err) != apperr.SettingsInvalid {
		t.Fatalf("%v", err)
	}
	if s.ServerAddress != "localhost:8443" || s.Version != 1 {
		t.Fatalf("при ошибке должны вернуться значения по умолчанию: %+v", s)
	}
}

func TestRecentLogins(t *testing.T) {
	var s Settings
	for _, l := range []string{"a1", "b2", "c3", "d4", "e5", "f6", "c3"} {
		s.AddRecentLogin(l)
	}
	want := []string{"c3", "f6", "e5", "d4", "b2"}
	if strings.Join(s.RecentLogins, ",") != strings.Join(want, ",") {
		t.Fatalf("%v", s.RecentLogins)
	}
}

func TestValidateAddress(t *testing.T) {
	for _, ok := range []string{"localhost:8443", "127.0.0.1:1", "[::1]:8443", "server:65535"} {
		if err := ValidateAddress(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "localhost", ":8443", "localhost:", "localhost:0", "localhost:70000", "localhost:abc", "a b:1", "http://x:1"} {
		if apperr.CodeOf(ValidateAddress(bad)) != apperr.InvalidAddress {
			t.Errorf("%q принят", bad)
		}
	}
}

func TestProfileDir(t *testing.T) {
	root := t.TempDir()
	a := DBPath(root, "localhost:8443", "u1")
	b := DBPath(root, "localhost:8443", "u2")
	c := DBPath(root, "127.0.0.1:8443", "u1")
	if a == b || a == c {
		t.Fatal("разные профили дают одну папку")
	}
	if a != DBPath(root, "localhost:8443", "u1") {
		t.Fatal("путь не постоянен")
	}
	id := filepath.Base(filepath.Dir(a))
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(id) || filepath.Base(a) != "client.db" ||
		filepath.Base(filepath.Dir(filepath.Dir(a))) != "profiles" {
		t.Fatalf("%s", a)
	}
}

func TestNoSecretsInFile(t *testing.T) {
	root := t.TempDir()
	s := Defaults()
	s.AddRecentLogin("alice")
	if err := Save(root, s); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "settings.json"))
	for _, k := range []string{"password", "token", "key"} {
		if strings.Contains(strings.ToLower(string(raw)), `"`+k) {
			t.Fatalf("в файле есть поле %s: %s", k, raw)
		}
	}
}
