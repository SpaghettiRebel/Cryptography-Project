package rpc

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	apiv1 "cryptochat/server/api/v1"
	"cryptochat/server/internal/contacts"
)

func expectError(t *testing.T, err error, code codes.Code, reason string) {
	t.Helper()
	if status.Code(err) != code || ReasonOf(err) != reason {
		t.Errorf("код %v, причина %q (%v); ожидалось %v, %q", status.Code(err), ReasonOf(err), err, code, reason)
	}
}

func TestAccountsFlow(t *testing.T) {
	h := start(t, nil)
	const pw = "correct-horse-battery"
	reg, err := h.accounts.Register(bg, &apiv1.RegisterRequest{Login: "Alice", Password: pw})
	if err != nil || reg.GetLogin() != "Alice" || reg.GetUserId() == "" {
		t.Fatalf("%+v, %v", reg, err)
	}
	_, err = h.accounts.Register(bg, &apiv1.RegisterRequest{Login: "alice", Password: pw})
	expectError(t, err, codes.AlreadyExists, ReasonLoginTaken)
	_, err = h.accounts.Register(bg, &apiv1.RegisterRequest{Login: "a", Password: pw})
	expectError(t, err, codes.InvalidArgument, ReasonInvalidLogin)
	_, err = h.accounts.Register(bg, &apiv1.RegisterRequest{Login: "bobby", Password: "short"})
	expectError(t, err, codes.InvalidArgument, ReasonInvalidPassword)

	login, err := h.accounts.Login(bg, &apiv1.LoginRequest{Login: "ALICE", Password: pw})
	if err != nil {
		t.Fatal(err)
	}
	if login.GetToken() == "" || login.GetUserId() != reg.GetUserId() || login.GetLogin() != "Alice" {
		t.Fatalf("%+v", login)
	}
	if d := time.Until(time.Unix(login.GetExpiresAt(), 0)); d < 23*time.Hour || d > 25*time.Hour {
		t.Fatalf("срок сессии %v", d)
	}

	if _, err := h.contacts.ListContacts(auth(login.GetToken()), &apiv1.ListContactsRequest{}); err != nil {
		t.Fatalf("защищённая операция: %v", err)
	}
	if _, err := h.accounts.Logout(auth(login.GetToken()), &apiv1.LogoutRequest{}); err != nil {
		t.Fatal(err)
	}
	_, err = h.contacts.ListContacts(auth(login.GetToken()), &apiv1.ListContactsRequest{})
	expectError(t, err, codes.Unauthenticated, ReasonUnauthenticated)
	_, err = h.accounts.Logout(auth(login.GetToken()), &apiv1.LogoutRequest{})
	expectError(t, err, codes.Unauthenticated, ReasonUnauthenticated)

	// Журнал не содержит ни пароля, ни сессии.
	log := h.log.String()
	if !strings.Contains(log, "/cryptochat.v1.Accounts/Login") {
		t.Fatal("журнал пуст")
	}
	if strings.Contains(log, pw) || strings.Contains(log, login.GetToken()) {
		t.Fatal("в журнале секреты")
	}
}

// Ответы на неверный пароль и неизвестный логин полностью совпадают.
func TestLoginAnswersEqual(t *testing.T) {
	h := start(t, nil)
	if _, err := h.accounts.Register(bg, &apiv1.RegisterRequest{Login: "alice", Password: "password-1"}); err != nil {
		t.Fatal(err)
	}
	_, wrong := h.accounts.Login(bg, &apiv1.LoginRequest{Login: "alice", Password: "not-the-password"})
	_, missing := h.accounts.Login(bg, &apiv1.LoginRequest{Login: "nobody", Password: "not-the-password"})
	expectError(t, wrong, codes.Unauthenticated, ReasonInvalidCredentials)
	if wrong.Error() != missing.Error() {
		t.Fatalf("ответы различаются:\n%v\n%v", wrong, missing)
	}
}

func TestSessionRejected(t *testing.T) {
	h := start(t, nil)
	_, err := h.contacts.ListContacts(bg, &apiv1.ListContactsRequest{})
	expectError(t, err, codes.Unauthenticated, ReasonUnauthenticated)
	_, err = h.contacts.ListContacts(auth("garbage-token"), &apiv1.ListContactsRequest{})
	expectError(t, err, codes.Unauthenticated, ReasonUnauthenticated)

	token, userID := h.signUp(t, "alice")
	_ = token
	const expired = "expired-token"
	sum := sha256.Sum256([]byte(expired))
	if err := h.store.CreateSession(bg, sum[:], userID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	_, err = h.contacts.ListContacts(auth(expired), &apiv1.ListContactsRequest{})
	expectError(t, err, codes.Unauthenticated, ReasonUnauthenticated)
}

func TestRequestTooLarge(t *testing.T) {
	h := start(t, nil)
	_, err := h.accounts.Register(bg, &apiv1.RegisterRequest{Login: "alice", Password: strings.Repeat("x", MaxMessageSize+1)})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("%v", err)
	}
	if _, err := h.accounts.Register(bg, &apiv1.RegisterRequest{Login: "alice", Password: "password-1"}); err != nil {
		t.Fatalf("сервер не работает после большого запроса: %v", err)
	}
}

func TestPanicIsRecovered(t *testing.T) {
	// Хранилище контактов равно nil: обработчик упадёт настоящей паникой.
	h := start(t, nil, func(d *Deps) { d.Contacts = contacts.New(nil) })
	token, _ := h.signUp(t, "alice")
	_, err := h.contacts.ListContacts(auth(token), &apiv1.ListContactsRequest{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(h.log.String(), "паника") {
		t.Fatal("паника не записана в журнал")
	}
	if _, err := h.accounts.Login(bg, &apiv1.LoginRequest{Login: "alice", Password: "password-for-tests"}); err != nil {
		t.Fatalf("сервер не работает после паники: %v", err)
	}
}

func TestClientCancel(t *testing.T) {
	h := start(t, nil)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if _, err := h.accounts.Register(ctx, &apiv1.RegisterRequest{Login: "alice", Password: "password-1"}); status.Code(err) != codes.Canceled {
		t.Fatalf("%v", err)
	}
	if _, err := h.accounts.Register(bg, &apiv1.RegisterRequest{Login: "bobby", Password: "password-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestHealthFollowsStore(t *testing.T) {
	var ready error
	h := start(t, func(context.Context) error { return ready })
	client := healthpb.NewHealthClient(h.conn)
	check := func() healthpb.HealthCheckResponse_ServingStatus {
		r, err := client.Check(bg, &healthpb.HealthCheckRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return r.GetStatus()
	}
	if got := check(); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("до первой проверки: %v", got)
	}
	h.server.CheckReady(bg)
	if got := check(); got != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("база доступна: %v", got)
	}
	ready = errors.New("нет базы")
	h.server.CheckReady(bg)
	if got := check(); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("база недоступна: %v", got)
	}
}
