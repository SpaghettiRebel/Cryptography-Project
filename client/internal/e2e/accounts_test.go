//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/localdb"
	"cryptochat/client/internal/profile"
	"cryptochat/client/internal/service"
)

// История 1, сценарии 1–4: регистрация, вход, причины отказа, выход.
func TestAccountsRegisterLoginLogout(t *testing.T) {
	a := newParticipant(t, "acc")
	a.register()
	if err := a.svc.Register(strings.ToUpper(a.login), a.password); apperr.CodeOf(err) != apperr.LoginTaken {
		t.Fatalf("занятый логин: %v", err)
	}
	_, wrongPw := a.svc.Login(a.login, "wrong-password-1")
	_, unknown := a.svc.Login(randomLogin("nobody"), a.password)
	e1, _ := apperr.As(wrongPw)
	e2, _ := apperr.As(unknown)
	if e1 == nil || e2 == nil || e1.Code != apperr.InvalidCredentials || *e1 != *e2 {
		t.Fatalf("сообщения о неверном входе различаются: %v / %v", wrongPw, unknown)
	}
	snap, err := a.svc.Login(a.login, a.password)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Session == nil || !strings.EqualFold(snap.Session.Login, a.login) || snap.Connection != service.Online {
		t.Fatalf("%+v", snap)
	}
	if err := a.svc.Logout(); err != nil {
		t.Fatal(err)
	}
	if s := a.svc.Snapshot(); s.Session != nil || len(s.Chats)+len(s.Contacts) != 0 {
		t.Fatalf("после выхода видны данные: %+v", s)
	}
	if e := a.rec.ended(); len(e) != 1 || e[0].Code != service.EndedLoggedOut {
		t.Fatalf("%+v", e)
	}
	settings, _ := profile.Load(a.home)
	if len(settings.RecentLogins) == 0 || settings.RecentLogins[0] != a.login {
		t.Fatalf("недавние логины: %v", settings.RecentLogins)
	}
}

// История 1, сценарий 5 и SC-010: два пользователя в одной папке данных не видят данных друг друга.
func TestAccountsTwoUsersOneComputer(t *testing.T) {
	a := user(t, "home_a")
	peer := user(t, "home_peer")
	befriend(t, a, peer)
	ch, err := a.svc.CreateChat(service.CreateChatRequest{Peer: peer.login, Algorithm: "RC6", KeyBits: 128, CipherMode: "CBC", Padding: "PKCS7"})
	var id [16]byte
	if err == nil {
		id = ch.ID
	} else {
		// В сборке без тестового шифра чат создаётся напрямую через API.
		id = rawLogin(t, a.login, a.password).createChat(peer.login, 2, 128, 2, 3)
		eventually(t, 5*time.Second, "чат у создателя", func() bool { _, ok := a.chat(id); return ok })
	}
	if err := a.svc.Logout(); err != nil {
		t.Fatal(err)
	}

	// Второй пользователь входит в той же папке данных.
	b := &participant{t: t, login: randomLogin("home_b"), password: "e2e-password-b", home: a.home}
	b.open()
	b.register().signIn()
	if s := b.svc.Snapshot(); len(s.Chats) != 0 || len(s.Contacts) != 0 {
		t.Fatalf("второй пользователь видит чужие данные: %+v", s)
	}
	_ = b.svc.Logout()
	entries, _ := os.ReadDir(filepath.Join(a.home, "profiles"))
	if len(entries) != 2 {
		t.Fatalf("папок профилей %d, ожидалось 2", len(entries))
	}

	// Первый возвращается к своим данным.
	a.restart()
	a.signIn()
	if _, ok := a.chat(id); !ok {
		t.Fatal("после повторного входа чат не виден")
	}
	if s := a.svc.Snapshot(); len(s.Contacts) != 1 {
		t.Fatalf("контакты: %v", s.Contacts)
	}
}

// История 1, сценарий 6: вторая копия с тем же профилем получает PROFILE_BUSY, разные профили работают
// одновременно.
func TestAccountsBusyProfile(t *testing.T) {
	a := user(t, "busy")
	second := &participant{t: t, login: a.login, password: a.password, home: a.home}
	second.open()
	if _, err := second.svc.Login(a.login, a.password); apperr.CodeOf(err) != apperr.ProfileBusy {
		t.Fatalf("вторая копия: %v", err)
	}
	if second.svc.Snapshot().Session != nil {
		t.Fatal("вторая копия вошла")
	}
	// Первая копия работает, данные целы.
	if _, err := a.svc.SendContactRequest(randomLogin("ghost")); apperr.CodeOf(err) != apperr.UserNotFound {
		t.Fatalf("первая копия: %v", err)
	}
	other := &participant{t: t, login: randomLogin("busy_other"), password: "e2e-password-o", home: a.home}
	other.svc = second.svc
	other.rec = second.rec
	other.register().signIn()
	if s := other.svc.Snapshot(); s.Session == nil {
		t.Fatal("другой профиль во второй копии не открылся")
	}
	a.svc.Close()
	other.svc.Close()
	db, err := localdb.Open(profile.DBPath(a.home, address, userID(t, a)))
	if err != nil {
		t.Fatalf("данные первой копии: %v", err)
	}
	_ = db.Close()
}

func userID(t *testing.T, p *participant) string {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(p.home, "profiles"))
	for _, e := range entries {
		db, err := localdb.Open(filepath.Join(p.home, "profiles", e.Name(), "client.db"))
		if err != nil {
			continue
		}
		id, login, err := db.Owner()
		_ = db.Close()
		if err == nil && strings.EqualFold(login, p.login) {
			return id
		}
	}
	t.Fatal("профиль не найден")
	return ""
}

// История 1, сценарий 7: неверный файл сертификата и чужой сертификат дают отдельные причины.
func TestAccountsCertificates(t *testing.T) {
	p := newParticipant(t, "cert")
	if _, err := p.svc.SaveSettings(profile.Settings{ServerAddress: address, CertificatePath: filepath.Join(t.TempDir(), "нет.crt")}); apperr.CodeOf(err) != apperr.CertFile {
		t.Fatalf("нет файла: %v", err)
	}
	foreign := filepath.Join(t.TempDir(), "foreign.crt")
	if err := os.WriteFile(foreign, foreignCert(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.svc.SaveSettings(profile.Settings{ServerAddress: address, CertificatePath: foreign}); err != nil {
		t.Fatal(err)
	}
	if err := p.svc.CheckServer(); apperr.CodeOf(err) != apperr.CertInvalid {
		t.Fatalf("чужой сертификат: %v", err)
	}
	if err := p.svc.Register(p.login, p.password); apperr.CodeOf(err) != apperr.CertInvalid {
		t.Fatalf("регистрация с чужим сертификатом: %v", err)
	}
	// Настройки сохранены между запусками; с верным сертификатом связь есть.
	if _, err := p.svc.SaveSettings(profile.Settings{ServerAddress: address, CertificatePath: certFile}); err != nil {
		t.Fatal(err)
	}
	p.restart()
	s, err := p.svc.GetSettings()
	if err != nil || s.CertificatePath != certFile || s.ServerAddress != address {
		t.Fatalf("%+v %v", s, err)
	}
	if err := p.svc.CheckServer(); err != nil {
		t.Fatalf("проверка связи: %v", err)
	}
}
