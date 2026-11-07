//go:build e2e

package e2e

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/profile"
	"cryptochat/client/internal/service"
	"cryptochat/core/cipher"
)

// История 5, сценарии 1–2 и SC-005, SC-006: сервер остановлен до входа и во время работы; после запуска
// работа восстанавливается без повторного входа.
func TestResilienceServerStopStart(t *testing.T) {
	a, b := user(t, "sa"), user(t, "sb")
	befriend(t, a, b)
	id := createChat(t, a, b, cipher.RC6, 128)
	eventually(t, 5*time.Second, "чат у обоих", func() bool { _, ok := b.chat(id); return ok })
	late := newParticipant(t, "late").register()

	stopServer(t)
	stopped := time.Now()
	t.Cleanup(func() {
		if t.Failed() {
			startServer(t)
		}
	})

	// Вход при остановленном сервере.
	start := time.Now()
	_, err := late.svc.Login(late.login, late.password)
	if apperr.CodeOf(err) != apperr.ServerUnavailable {
		t.Fatalf("вход без сервера: %v", err)
	}
	t.Logf("SC-005: сообщение о недоступном сервере при входе через %v", time.Since(start).Round(time.Millisecond))
	if time.Since(start) > 5*time.Second {
		t.Fatal("сообщение позже 5 с")
	}

	// Обрыв во время работы.
	d := eventually(t, 5*time.Second, "offline", func() bool { return a.svc.Snapshot().Connection == service.Offline })
	t.Logf("SC-005: индикатор «нет связи» через %v после остановки", (time.Since(stopped)).Round(time.Millisecond))
	_ = d
	if _, ok := a.chat(id); !ok || a.svc.Snapshot().Session == nil {
		t.Fatal("без связи пропали локальные данные или сессия")
	}
	if len(a.rec.notices(apperr.ServerUnavailable)) == 0 {
		t.Fatal("нет сообщения о потере связи")
	}
	if _, err := a.svc.SendContactRequest(late.login); apperr.CodeOf(err) != apperr.ServerUnavailable {
		t.Fatalf("действие без связи: %v", err)
	}

	// Изменение, которое клиент увидит только после восстановления.
	mustCompose(t, "start", "server")
	started := time.Now()
	if err := waitHealthy(2 * time.Minute); err != nil {
		t.Fatal(err)
	}
	healthy := time.Since(started)
	if err := b.svc.CloseChat(hex.EncodeToString(id[:])); err != nil {
		t.Fatalf("закрытие после запуска: %v", err)
	}
	d = eventually(t, 10*time.Second, "online и обновлённый список", func() bool {
		_, ok := a.chat(id)
		return a.svc.Snapshot().Connection == service.Online && !ok
	})
	t.Logf("SC-006: сервер готов через %v после start; клиент в сети с обновлённым списком через %v после готовности",
		healthy.Round(time.Millisecond), d.Round(time.Millisecond))
	if a.svc.Snapshot().Session == nil || len(a.rec.ended()) != 0 {
		t.Fatal("потребовался повторный вход")
	}
	if len(a.rec.notices("CONNECTION_RESTORED")) == 0 {
		t.Fatal("нет сообщения о восстановлении связи")
	}
	if _, err := late.svc.Login(late.login, late.password); err != nil {
		t.Fatalf("вход после запуска сервера: %v", err)
	}
}

// История 5, сценарий 3: сессия отозвана — событие session-ended, после входа данные на месте.
func TestResilienceSessionRevoked(t *testing.T) {
	a, b := user(t, "rva"), user(t, "rvb")
	befriend(t, a, b)
	id := createChat(t, a, b, cipher.MAGENTA, 256)
	eventually(t, 5*time.Second, "чат", func() bool { _, ok := a.chat(id); return ok })
	rawClient(t, a.spy.lastToken()).logout()
	d := eventually(t, 5*time.Second, "session-ended", func() bool { return len(a.rec.ended()) == 1 })
	t.Logf("SC-005: отзыв сессии замечен через %v", d.Round(time.Millisecond))
	e := a.rec.ended()[0]
	if e.Code != service.EndedExpired || !strings.EqualFold(e.Login, a.login) {
		t.Fatalf("%+v", e)
	}
	if a.svc.Snapshot().Session != nil {
		t.Fatal("сессия не сброшена")
	}
	eventually(t, 5*time.Second, "повторный вход", func() bool {
		_, err := a.svc.Login(a.login, a.password)
		return err == nil
	})
	if _, ok := a.chat(id); !ok || len(a.svc.Snapshot().Contacts) != 1 {
		t.Fatal("после повторного входа данные не на месте")
	}
}

// История 5, сценарий 6: повреждённая база профиля — понятное сообщение с путём, приложение работает.
func TestResilienceCorruptedProfile(t *testing.T) {
	a := user(t, "corrupt")
	a.svc.Close()
	uid := userID(t, a)
	path := profile.DBPath(a.home, address, uid)
	_ = os.Remove(path + "-wal")
	if err := os.WriteFile(path, []byte(strings.Repeat("испорчено ", 2000)), 0o600); err != nil {
		t.Fatal(err)
	}
	a.open()
	_, err := a.svc.Login(a.login, a.password)
	e, ok := apperr.As(err)
	if !ok || e.Code != apperr.ProfileStorage || !strings.Contains(e.Message, filepath.Dir(path)) {
		t.Fatalf("%v", err)
	}
	// Окно продолжает работать: проверка связи проходит.
	if err := a.svc.CheckServer(); err != nil {
		t.Fatal(err)
	}
}

// SC-004: после сценариев согласования журналы сервера, дамп его базы и все снимки для интерфейса не
// содержат сеансовых ключей и секретных значений сторон.
func TestResilienceNoSecretsOnServer(t *testing.T) {
	a, b := user(t, "sca"), user(t, "scb")
	befriend(t, a, b)
	id := createChat(t, a, b, cipher.RC6, 256)
	eventually(t, 5*time.Second, "чат у приглашённого", func() bool { _, ok := b.chat(id); return ok })
	connect(t, b, id)
	waitReady(t, id, a, b)
	var snapshots []string
	for _, p := range []*participant{a, b} {
		for _, e := range p.rec.all() {
			raw, err := json.Marshal(e.data)
			if err != nil {
				t.Fatal(err)
			}
			snapshots = append(snapshots, string(raw))
		}
	}
	storedKey(t, a, id)
	storedKey(t, b, id)
	values := allSecrets()
	if len(values) < 4 {
		t.Fatalf("перехвачено значений: %d", len(values))
	}
	logs, dump := serverLogs(t), pgDump(t)
	hits := 0
	for _, v := range values {
		for _, needle := range []string{hex.EncodeToString(v), strings.ToUpper(hex.EncodeToString(v)),
			base64.StdEncoding.EncodeToString(v), base64.RawStdEncoding.EncodeToString(v), base64.RawURLEncoding.EncodeToString(v)} {
			for what, text := range map[string]string{"журнал сервера": logs, "pg_dump": dump, "снимки": strings.Join(snapshots, "\n")} {
				if strings.Contains(text, needle) {
					hits++
					t.Errorf("%s содержит секрет", what)
				}
			}
		}
	}
	t.Logf("SC-004: проверено значений: %d (секреты и ключи всех тестов пакета), журнал %d байт, pg_dump %d байт, снимков %d; совпадений: %d",
		len(values), len(logs), len(dump), len(snapshots), hits)
}
