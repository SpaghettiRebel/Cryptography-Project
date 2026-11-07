package service

import (
	"strings"
	"testing"
	"time"

	"cryptochat/client/internal/apitest"
	"cryptochat/client/internal/apperr"
)

// Паника в фоновой задаче не завершает процесс: в журнале нет значения паники, интерфейс получает
// уведомление INTERNAL.
func TestBackgroundPanicBecomesNotice(t *testing.T) {
	p := newParty(t, apitest.NewServer(), t.TempDir())
	p.svc.goSafe("проверка", func() { panic("значение паники 5ec2e7") })
	eventually(t, 2*time.Second, "уведомление INTERNAL", func() bool { return len(p.rec.notices(apperr.Internal)) == 1 })
	if strings.Contains(p.logs.String(), "5ec2e7") {
		t.Fatal("значение паники попало в журнал")
	}
	if !strings.Contains(p.logs.String(), "внутренняя ошибка (проверка)") {
		t.Fatalf("нет записи в журнале: %s", p.logs.String())
	}
	// После паники сервис работает.
	p.svc.goSafe("вторая", func() {})
	if s := p.svc.Snapshot(); s.Session != nil {
		t.Fatal("сессии быть не должно")
	}
}

func TestEmitterPanicIgnored(t *testing.T) {
	p := newParty(t, apitest.NewServer(), t.TempDir())
	p.svc.Subscribe(EmitterFunc(func(string, any) { panic("получатель") }))
	p.svc.notice(NoticeInfo, "X", "сообщение")
	if len(p.rec.notices("X")) != 1 {
		t.Fatal("второй получатель не получил событие")
	}
}

func TestCloseIdempotent(t *testing.T) {
	p := newParty(t, apitest.NewServer(), t.TempDir())
	p.svc.Close()
	p.svc.Close()
}
