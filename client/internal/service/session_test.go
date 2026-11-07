package service

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cryptochat/client/internal/apitest"
	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/localdb"
	"cryptochat/client/internal/profile"
	"cryptochat/client/internal/rpcclient"
)

func TestLocalValidationBeforeServer(t *testing.T) {
	p := newParty(t, apitest.NewServer(), t.TempDir())
	cases := []struct {
		login, password, code, field string
	}{
		{"ab", testPassword, apperr.InvalidLogin, "login"},
		{strings.Repeat("a", 33), testPassword, apperr.InvalidLogin, "login"},
		{"кириллица", testPassword, apperr.InvalidLogin, "login"},
		{"with space", testPassword, apperr.InvalidLogin, "login"},
		{"", testPassword, apperr.InvalidLogin, "login"},
		{"alice", "short", apperr.InvalidPassword, "password"},
		{"alice", strings.Repeat("x", 129), apperr.InvalidPassword, "password"},
		{"alice", "", apperr.InvalidPassword, "password"},
	}
	for _, c := range cases {
		for name, call := range map[string]func() error{
			"Register": func() error { return p.svc.Register(c.login, c.password) },
			"Login": func() error {
				_, err := p.svc.Login(c.login, c.password)
				return err
			},
		} {
			e, ok := apperr.As(call())
			if !ok || e.Code != c.code || e.Field != c.field {
				t.Errorf("%s(%q, %d байт): %v", name, c.login, len(c.password), e)
			}
		}
	}
	if n := len(p.api.Calls()); n != 0 {
		t.Fatalf("неверные данные дошли до сервера: %v", p.api.Calls())
	}
	// Граничные значения допустимы: 3 и 32 символа, 8 и 128 байт, пробелы в пароле.
	for _, ok := range [][2]string{{"a.b", "12345678"}, {strings.Repeat("z", 32), strings.Repeat("п", 64)}, {"x_y-z", "пароль с пробелами"}} {
		if err := p.svc.Register(ok[0], ok[1]); err != nil {
			t.Errorf("%q: %v", ok[0], err)
		}
	}
}

func TestRegisterLoginLogout(t *testing.T) {
	srv := apitest.NewServer()
	p := newParty(t, srv, t.TempDir())
	if err := p.svc.Register("alice", testPassword); err != nil {
		t.Fatal(err)
	}
	if apperr.CodeOf(p.svc.Register("ALICE", testPassword)) != apperr.LoginTaken {
		t.Fatal("занятый логин")
	}
	if _, err := p.svc.Login("alice", "wrong-password"); apperr.CodeOf(err) != apperr.InvalidCredentials {
		t.Fatalf("неверный пароль: %v", err)
	}
	if _, err := p.svc.Login("nobody", testPassword); apperr.CodeOf(err) != apperr.InvalidCredentials {
		t.Fatalf("неизвестный логин: %v", err)
	}
	snap, err := p.svc.Login("alice", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Session == nil || snap.Session.Login != "alice" || snap.Connection != Online {
		t.Fatalf("%+v", snap)
	}
	settings, err := profile.Load(p.root)
	if err != nil || len(settings.RecentLogins) != 1 || settings.RecentLogins[0] != "alice" {
		t.Fatalf("недавние логины: %+v %v", settings, err)
	}
	dbPath := profile.DBPath(p.root, "localhost:8443", "id-alice")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("база профиля не создана: %v", err)
	}
	// Повторный вход без выхода не нужен: действия требуют сессию, выход её завершает.
	if err := p.svc.Logout(); err != nil {
		t.Fatal(err)
	}
	if s := p.svc.Snapshot(); s.Session != nil || len(s.Chats) != 0 || len(s.Contacts) != 0 {
		t.Fatalf("после выхода остались данные: %+v", s)
	}
	ended := p.rec.ended()
	if len(ended) != 1 || ended[0].Code != EndedLoggedOut || ended[0].Login != "alice" {
		t.Fatalf("%+v", ended)
	}
	if p.api.Token() != "" {
		t.Fatal("токен остался в соединении")
	}
	// База закрыта: её можно открыть.
	db, err := localdb.Open(dbPath)
	if err != nil {
		t.Fatalf("база не закрыта после выхода: %v", err)
	}
	_ = db.Close()
	if _, err := p.svc.SendContactRequest("bob"); apperr.CodeOf(err) != apperr.NotLoggedIn {
		t.Fatalf("действие без входа: %v", err)
	}
}

func TestLoginCaseGivesSameProfile(t *testing.T) {
	srv := apitest.NewServer()
	p := newParty(t, srv, t.TempDir())
	if err := p.svc.Register("Alice", testPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := p.svc.Login("ALICE", testPassword); err != nil {
		t.Fatal(err)
	}
	_ = p.svc.Logout()
	if _, err := p.svc.Login("alice", testPassword); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(p.root, "profiles"))
	if len(entries) != 1 {
		t.Fatalf("папок профиля %d, ожидалась одна", len(entries))
	}
}

func TestLogoutServerErrorDoesNotBlock(t *testing.T) {
	srv := apitest.NewServer()
	p := registered(t, srv, "alice")
	srv.SetDown(true)
	if err := p.svc.Logout(); err != nil {
		t.Fatalf("выход при недоступном сервере: %v", err)
	}
	if p.svc.Snapshot().Session != nil {
		t.Fatal("сессия не завершена")
	}
}

func TestBusyProfile(t *testing.T) {
	srv := apitest.NewServer()
	root := t.TempDir()
	first := newParty(t, srv, root)
	if err := first.svc.Register("alice", testPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := first.svc.Login("alice", testPassword); err != nil {
		t.Fatal(err)
	}
	second := newParty(t, srv, root)
	if _, err := second.svc.Login("alice", testPassword); apperr.CodeOf(err) != apperr.ProfileBusy {
		t.Fatalf("вторая копия: %v", err)
	}
	if second.svc.Snapshot().Session != nil {
		t.Fatal("вторая копия вошла")
	}
	if second.api.Count("Logout") != 1 {
		t.Fatalf("полученная сессия не отозвана: %v", second.api.Calls())
	}
	// Первая копия работает.
	if _, err := first.svc.SendContactRequest("alice"); apperr.CodeOf(err) != apperr.SelfRequest {
		t.Fatalf("первая копия: %v", err)
	}
	// Другой пользователь во второй копии входит.
	if err := second.svc.Register("bob", testPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := second.svc.Login("bob", testPassword); err != nil {
		t.Fatalf("другой профиль: %v", err)
	}
}

func TestCorruptedProfile(t *testing.T) {
	srv := apitest.NewServer()
	p := newParty(t, srv, t.TempDir())
	if err := p.svc.Register("alice", testPassword); err != nil {
		t.Fatal(err)
	}
	path := profile.DBPath(p.root, "localhost:8443", "id-alice")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("мусор", 1000)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := p.svc.Login("alice", testPassword)
	e, ok := apperr.As(err)
	if !ok || e.Code != apperr.ProfileStorage || !strings.Contains(e.Message, filepath.Dir(path)) {
		t.Fatalf("%v", err)
	}
	if p.svc.Snapshot().Session != nil || p.api.Count("Logout") != 1 {
		t.Fatal("сессия должна быть отозвана")
	}
}

func TestSecretsNotStored(t *testing.T) {
	srv := apitest.NewServer()
	const pw = "unique-password-7f3a"
	p := newParty(t, srv, t.TempDir())
	if err := p.svc.Register("alice", pw); err != nil {
		t.Fatal(err)
	}
	if _, err := p.svc.Login("alice", pw); err != nil {
		t.Fatal(err)
	}
	token := p.api.Token()
	raw, _ := os.ReadFile(filepath.Join(p.root, "settings.json"))
	snap, _ := json.Marshal(p.svc.Snapshot())
	for what, text := range map[string]string{"settings.json": string(raw), "журнал": p.logs.String(), "снимок": string(snap)} {
		if containsAny(text, pw, token) {
			t.Errorf("%s содержит пароль или токен", what)
		}
	}
	for _, e := range p.rec.all() {
		b, _ := json.Marshal(e.data)
		if containsAny(string(b), pw, token) {
			t.Errorf("событие %s содержит пароль или токен", e.name)
		}
	}
}

func TestSettings(t *testing.T) {
	srv := apitest.NewServer()
	p := newParty(t, srv, t.TempDir())
	s, err := p.svc.GetSettings()
	if err != nil || s.ServerAddress != "localhost:8443" {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := p.svc.SaveSettings(profile.Settings{ServerAddress: "localhost"}); apperr.CodeOf(err) != apperr.InvalidAddress {
		t.Fatalf("адрес без порта: %v", err)
	}
	if _, err := p.svc.SaveSettings(profile.Settings{ServerAddress: "localhost:1", CertificatePath: filepath.Join(t.TempDir(), "нет.crt")}); apperr.CodeOf(err) != apperr.CertFile {
		t.Fatalf("нет файла сертификата: %v", err)
	}
	cert := filepath.Join(t.TempDir(), "server.crt")
	_ = os.WriteFile(cert, testPEM(t), 0o600)
	saved, err := p.svc.SaveSettings(profile.Settings{ServerAddress: "127.0.0.1:18445", CertificatePath: cert})
	if err != nil || saved.ServerAddress != "127.0.0.1:18445" {
		t.Fatalf("%+v %v", saved, err)
	}
	// Настройки переживают перезапуск.
	q := newParty(t, srv, p.root)
	got, err := q.svc.GetSettings()
	if err != nil || got.ServerAddress != "127.0.0.1:18445" || got.CertificatePath != cert {
		t.Fatalf("%+v %v", got, err)
	}
	if err := q.svc.CheckServer(); err != nil {
		t.Fatalf("проверка связи: %v", err)
	}
	srv.SetDown(true)
	if err := q.svc.CheckServer(); apperr.CodeOf(err) != apperr.ServerUnavailable {
		t.Fatalf("сервер недоступен: %v", err)
	}
}

func TestCorruptedSettingsNotice(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte("{испорчен"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(Config{Root: root, Logger: log.New(io.Discard, "", 0)})
	rec := &recorder{}
	r.Subscribe(rec)
	defer r.Close()
	s, err := r.GetSettings()
	if err != nil || s.ServerAddress != "localhost:8443" {
		t.Fatalf("%+v %v", s, err)
	}
	if len(rec.notices(apperr.SettingsInvalid)) != 1 {
		t.Fatal("нет уведомления о сброшенных настройках")
	}
}

func TestDialErrorsReachUser(t *testing.T) {
	p := newParty(t, apitest.NewServer(), t.TempDir())
	p.svc.cfg.Dial = func(string, string) (rpcclient.API, error) { return nil, apperr.New(apperr.CertFile) }
	if err := p.svc.Register("alice", testPassword); apperr.CodeOf(err) != apperr.CertFile {
		t.Fatalf("%v", err)
	}
	if _, err := p.svc.Login("alice", testPassword); apperr.CodeOf(err) != apperr.CertFile {
		t.Fatalf("%v", err)
	}
}

// Файл сертификата заменён по тому же пути: следующая проверка связи открывает соединение с новым
// сертификатом — после сохранения настроек и без него, вне сессии и в сессии.
func TestReplacedCertificateReconnects(t *testing.T) {
	srv := apitest.NewServer()
	p := newParty(t, srv, t.TempDir())
	var (
		mu     sync.Mutex
		dialed []string
	)
	p.svc.cfg.Dial = func(_ string, cert string) (rpcclient.API, error) {
		raw, err := os.ReadFile(cert)
		if err != nil {
			return nil, apperr.New(apperr.CertFile)
		}
		mu.Lock()
		dialed = append(dialed, string(raw))
		mu.Unlock()
		return p.api, nil
	}
	last := func() string {
		mu.Lock()
		defer mu.Unlock()
		if len(dialed) == 0 {
			return ""
		}
		return dialed[len(dialed)-1]
	}
	cert := filepath.Join(t.TempDir(), "server.crt")
	write := func() string {
		pemBytes := testPEM(t)
		if err := os.WriteFile(cert, pemBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		return string(pemBytes)
	}
	settings := profile.Settings{ServerAddress: "localhost:8443", CertificatePath: cert}

	first := write()
	if _, err := p.svc.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := p.svc.CheckServer(); err != nil || last() != first {
		t.Fatalf("первая проверка: %v", err)
	}
	// Тот же сертификат — соединение не пересоздаётся.
	if _, err := p.svc.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(dialed)
	}
	n := count()
	if err := p.svc.CheckServer(); err != nil || count() != n {
		t.Fatalf("лишнее соединение: %v, %d → %d", err, n, count())
	}

	second := write()
	if _, err := p.svc.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := p.svc.CheckServer(); err != nil || last() != second {
		t.Fatalf("после сохранения настроек проверка идёт со старым сертификатом: %v", err)
	}

	third := write()
	if err := p.svc.CheckServer(); err != nil || last() != third {
		t.Fatalf("проверка без сохранения идёт со старым сертификатом: %v", err)
	}

	if err := p.svc.Register("alice", testPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := p.svc.Login("alice", testPassword); err != nil {
		t.Fatal(err)
	}
	fourth := write()
	if err := p.svc.CheckServer(); err != nil || last() != fourth {
		t.Fatalf("проверка в сессии идёт со старым сертификатом: %v", err)
	}
	if p.svc.Snapshot().Session == nil {
		t.Fatal("проверка связи завершила сессию")
	}
}

// Два одновременных входа: остаётся одна сессия, цикл и база другой завершены.
func TestConcurrentLogins(t *testing.T) {
	srv := apitest.NewServer()
	p := newParty(t, srv, t.TempDir())
	for _, l := range []string{"alice", "bob"} {
		if err := p.svc.Register(l, testPassword); err != nil {
			t.Fatal(err)
		}
	}
	// Ответ сервера на вход задерживается, чтобы оба входа шли одновременно.
	p.api.SetHook(func(m string) error {
		if m == "Login" {
			time.Sleep(50 * time.Millisecond)
		}
		return nil
	})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, l := range []string{"alice", "bob"} {
		wg.Add(1)
		go func(i int, l string) {
			defer wg.Done()
			_, errs[i] = p.svc.Login(l, testPassword)
		}(i, l)
	}
	wg.Wait()
	p.api.SetHook(nil)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("вход %d: %v", i, err)
		}
	}
	cur := p.svc.Snapshot().Session
	if cur == nil {
		t.Fatal("нет сессии")
	}
	// Первая сессия завершена вторым входом: её база закрыта, о завершении сообщено.
	other := "id-alice"
	if cur.UserID == other {
		other = "id-bob"
	}
	db, err := localdb.Open(profile.DBPath(p.root, "localhost:8443", other))
	if err != nil {
		t.Fatalf("база первой сессии не закрыта: %v", err)
	}
	_ = db.Close()
	if e := p.rec.ended(); len(e) != 1 || e[0].Code != EndedLoggedOut {
		t.Fatalf("завершение первой сессии: %+v", e)
	}
	if err := p.svc.Logout(); err != nil {
		t.Fatal(err)
	}
	// Базы обоих профилей закрыты: открытая база осталась бы у потерянной сессии.
	for _, id := range []string{"id-alice", "id-bob"} {
		db, err := localdb.Open(profile.DBPath(p.root, "localhost:8443", id))
		if err != nil {
			t.Fatalf("база %s не закрыта: %v", id, err)
		}
		_ = db.Close()
	}
	// После выхода циклов синхронизации нет.
	n := p.api.Count("ListContacts")
	time.Sleep(300 * time.Millisecond)
	if m := p.api.Count("ListContacts"); m != n {
		t.Fatalf("цикл синхронизации остался после выхода: %d → %d вызовов", n, m)
	}
}
