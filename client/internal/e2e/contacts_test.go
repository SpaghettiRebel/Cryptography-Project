//go:build e2e

package e2e

import (
	"slices"
	"strings"
	"testing"
	"time"

	apiv1 "cryptochat/client/internal/api/v1"
	"cryptochat/client/internal/apperr"
)

func hasLogin(list []string, login string) bool {
	return slices.ContainsFunc(list, func(s string) bool { return strings.EqualFold(s, login) })
}

// История 2, сценарии 1–2 и SC-007: запрос виден у второго участника не позже 5 с, принятие и
// отклонение видны у обоих.
func TestContactsRequestAcceptReject(t *testing.T) {
	a, b, c := user(t, "ca"), user(t, "cb"), user(t, "cc")
	if _, err := a.svc.SendContactRequest(b.login); err != nil {
		t.Fatal(err)
	}
	if !hasLogin(a.svc.Snapshot().Outgoing, b.login) {
		t.Fatal("исходящий запрос не виден у отправителя")
	}
	d := eventually(t, 5*time.Second, "входящий запрос у получателя", func() bool { return hasLogin(b.svc.Snapshot().Incoming, a.login) })
	t.Logf("SC-007: входящий запрос виден через %v", d.Round(time.Millisecond))
	if err := b.svc.AcceptContact(a.login); err != nil {
		t.Fatal(err)
	}
	d = eventually(t, 5*time.Second, "контакт у отправителя", func() bool { return hasLogin(a.svc.Snapshot().Contacts, b.login) })
	t.Logf("SC-007: принятие видно через %v", d.Round(time.Millisecond))
	if !hasLogin(b.svc.Snapshot().Contacts, a.login) {
		t.Fatal("контакт не виден у принявшего")
	}

	if _, err := a.svc.SendContactRequest(c.login); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "входящий у c", func() bool { return hasLogin(c.svc.Snapshot().Incoming, a.login) })
	if err := c.svc.RejectContact(a.login); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "запрос исчез у отправителя", func() bool { return !hasLogin(a.svc.Snapshot().Outgoing, c.login) })
	if hasLogin(a.svc.Snapshot().Contacts, c.login) || hasLogin(c.svc.Snapshot().Contacts, a.login) {
		t.Fatal("после отклонения создан контакт")
	}
}

// История 2, сценарии 3–4: встречный запрос и отдельные причины отказа.
func TestContactsMutualAndErrors(t *testing.T) {
	a, b := user(t, "ma"), user(t, "mb")
	if _, err := a.svc.SendContactRequest(b.login); err != nil {
		t.Fatal(err)
	}
	became, err := b.svc.SendContactRequest(a.login)
	if err != nil || !became {
		t.Fatalf("встречный запрос: %v %v", became, err)
	}
	if len(b.rec.notices("CONTACT_CREATED")) != 1 {
		t.Fatal("нет сообщения о созданном контакте")
	}
	cases := []struct {
		login, code string
	}{
		{randomLogin("ghost"), apperr.UserNotFound},
		{strings.ToUpper(a.login), apperr.SelfRequest},
		{b.login, apperr.AlreadyContact},
	}
	for _, c := range cases {
		if _, err := a.svc.SendContactRequest(c.login); apperr.CodeOf(err) != c.code {
			t.Errorf("%s: %v", c.code, err)
		}
	}
	d := user(t, "md")
	if _, err := a.svc.SendContactRequest(d.login); err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.SendContactRequest(d.login); apperr.CodeOf(err) != apperr.DuplicateRequest {
		t.Errorf("повторный запрос: %v", err)
	}
	msgs := map[string]bool{}
	for _, code := range []string{apperr.UserNotFound, apperr.SelfRequest, apperr.AlreadyContact, apperr.DuplicateRequest} {
		msgs[apperr.New(code).Message] = true
	}
	if len(msgs) != 4 {
		t.Fatal("сообщения причин совпадают")
	}
}

// История 2, сценарий 5: удаление контакта закрывает его активные чаты у обоих.
func TestContactsRemoveClosesChats(t *testing.T) {
	a, b := user(t, "ra"), user(t, "rb")
	befriend(t, a, b)
	id := rawLogin(t, a.login, a.password).createChat(b.login, apiv1.Algorithm_ALGORITHM_RC6, 256,
		apiv1.CipherMode_CIPHER_MODE_CBC, apiv1.PaddingMode_PADDING_MODE_PKCS7)
	eventually(t, 5*time.Second, "чат у обоих", func() bool {
		_, okA := a.chat(id)
		_, okB := b.chat(id)
		return okA && okB
	})
	if err := a.svc.RemoveContact(b.login); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.chat(id); ok {
		t.Fatal("чат удалившего остался активным")
	}
	d := eventually(t, 5*time.Second, "чат исчез у собеседника", func() bool { _, ok := b.chat(id); return !ok })
	t.Logf("SC-007: закрытие чата удалением контакта видно через %v", d.Round(time.Millisecond))
	eventually(t, 5*time.Second, "контакт исчез у собеседника", func() bool { return !hasLogin(b.svc.Snapshot().Contacts, a.login) })
	if len(a.rec.notices("CHAT_CLOSED_BY_PEER")) != 0 {
		t.Fatal("удалившему пришло уведомление о закрытии собеседником")
	}
}
