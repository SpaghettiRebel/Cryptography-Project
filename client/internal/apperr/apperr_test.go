package apperr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func serverErr(t *testing.T, c codes.Code, reason string, meta map[string]string) error {
	t.Helper()
	st := status.New(c, "текст сервера с секретом 0123abcd")
	if reason == "" {
		return st.Err()
	}
	st, err := st.WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: "cryptochat", Metadata: meta})
	if err != nil {
		t.Fatal(err)
	}
	return st.Err()
}

func TestFromStatusTable(t *testing.T) {
	cases := []struct {
		code   codes.Code
		reason string
		meta   map[string]string
		want   string
		field  string
	}{
		{codes.AlreadyExists, "LOGIN_TAKEN", nil, LoginTaken, ""},
		{codes.InvalidArgument, "INVALID_LOGIN", nil, InvalidLogin, "login"},
		{codes.InvalidArgument, "INVALID_PASSWORD", nil, InvalidPassword, "password"},
		{codes.Unauthenticated, "INVALID_CREDENTIALS", nil, InvalidCredentials, ""},
		{codes.Unauthenticated, "UNAUTHENTICATED", nil, SessionExpired, ""},
		{codes.NotFound, "USER_NOT_FOUND", nil, UserNotFound, ""},
		{codes.InvalidArgument, "SELF_REQUEST", nil, SelfRequest, ""},
		{codes.AlreadyExists, "ALREADY_CONTACT", nil, AlreadyContact, ""},
		{codes.AlreadyExists, "DUPLICATE_REQUEST", nil, DuplicateRequest, ""},
		{codes.NotFound, "REQUEST_NOT_FOUND", nil, RequestNotFound, ""},
		{codes.NotFound, "CONTACT_NOT_FOUND", nil, ContactNotFound, ""},
		{codes.FailedPrecondition, "NOT_CONTACT", nil, NotContact, ""},
		{codes.InvalidArgument, "SELF_CHAT", nil, SelfChat, ""},
		{codes.InvalidArgument, "INVALID_CHAT_PARAMS", map[string]string{"field": "cipher_mode"}, InvalidChatParams, "cipher_mode"},
		{codes.InvalidArgument, "INVALID_CHAT_ID", nil, InvalidChatID, ""},
		{codes.NotFound, "CHAT_NOT_FOUND", nil, ChatNotFound, ""},
		{codes.FailedPrecondition, "CHAT_CLOSED", nil, ChatClosed, ""},
		{codes.FailedPrecondition, "NOT_CONNECTED", nil, NotConnected, ""},
		{codes.AlreadyExists, "VALUE_ALREADY_PUBLISHED", nil, KeyLost, ""},
		{codes.InvalidArgument, "INVALID_PUBLIC_VALUE", nil, Internal, ""},
		{codes.Unavailable, "STORE_UNAVAILABLE", nil, ServerBusy, ""},
		{codes.Unavailable, "BROKER_UNAVAILABLE", nil, ServerBusy, ""},
		{codes.Unavailable, "SERVER_STOPPING", nil, ServerBusy, ""},
		{codes.Unavailable, "", nil, ServerUnavailable, ""},
		{codes.DeadlineExceeded, "", nil, Timeout, ""},
		{codes.ResourceExhausted, "QUEUE_FULL", nil, ServerBusy, ""},
		{codes.FailedPrecondition, "CHAT_NOT_READY", nil, ServerBusy, ""},
		{codes.Aborted, "STREAM_REPLACED", nil, ServerBusy, ""},
		{codes.DeadlineExceeded, "ACK_TIMEOUT", nil, ServerBusy, ""},
		{codes.Internal, "", nil, Internal, ""},
		{codes.Unknown, "СТРАННАЯ_ПРИЧИНА", nil, Internal, ""},
		{codes.NotFound, "", nil, Internal, ""},
	}
	for _, c := range cases {
		name := c.code.String() + "/" + c.reason
		got := FromStatus(serverErr(t, c.code, c.reason, c.meta))
		if got.Code != c.want || got.Field != c.field {
			t.Errorf("%s: %+v, ожидалось %s/%s", name, got, c.want, c.field)
		}
		if got.Message == "" || got.Message != messages[got.Code] {
			t.Errorf("%s: сообщение %q", name, got.Message)
		}
		if strings.Contains(got.Error(), "0123abcd") {
			t.Errorf("%s: текст сервера попал в ошибку", name)
		}
	}
}

func TestOtherDomainIgnored(t *testing.T) {
	st, _ := status.New(codes.NotFound, "x").WithDetails(&errdetails.ErrorInfo{Reason: "CHAT_NOT_FOUND", Domain: "чужой"})
	if got := FromStatus(st.Err()); got.Code != Internal {
		t.Fatalf("причина чужого домена учтена: %s", got.Code)
	}
}

func TestNonStatusErrors(t *testing.T) {
	if got := FromStatus(errors.New("что-то")); got.Code != Internal {
		t.Fatal(got.Code)
	}
	if got := FromStatus(context.DeadlineExceeded); got.Code != Timeout {
		t.Fatal(got.Code)
	}
	if got := FromStatus(nil); got != nil {
		t.Fatal("nil должен давать nil")
	}
	own := New(ProfileBusy)
	if got := FromStatus(fmt.Errorf("обёртка: %w", own)); got != own {
		t.Fatal("собственная ошибка должна возвращаться как есть")
	}
}

func TestErrorJSON(t *testing.T) {
	e := WithField(InvalidChatParams, "key_bits")
	var got map[string]string
	if err := json.Unmarshal([]byte(e.Error()), &got); err != nil {
		t.Fatal(err)
	}
	if got["code"] != InvalidChatParams || got["message"] != messages[InvalidChatParams] || got["field"] != "key_bits" {
		t.Fatalf("%v", got)
	}
	var noField map[string]any
	_ = json.Unmarshal([]byte(New(Internal).Error()), &noField)
	if _, ok := noField["field"]; ok {
		t.Fatal("пустое поле field не должно выводиться")
	}
	if CodeOf(fmt.Errorf("x: %w", e)) != InvalidChatParams || CodeOf(errors.New("x")) != Internal || CodeOf(nil) != "" {
		t.Fatal("CodeOf")
	}
}

func TestEveryCodeHasMessage(t *testing.T) {
	for _, c := range []string{NotLoggedIn, InvalidAddress, CertFile, CertInvalid, ProfileBusy, ProfileStorage,
		SettingsInvalid, AlgorithmUnavailable, PeerValueInvalid, Internal, LoginTaken, InvalidLogin, InvalidPassword,
		InvalidCredentials, SessionExpired, UserNotFound, SelfRequest, AlreadyContact, DuplicateRequest,
		RequestNotFound, ContactNotFound, NotContact, SelfChat, InvalidChatParams, InvalidChatID, ChatNotFound,
		ChatClosed, NotConnected, KeyLost, ServerBusy, ServerUnavailable, Timeout} {
		if messages[c] == "" {
			t.Errorf("нет сообщения для %s", c)
		}
	}
	p := Profile(`C:\data\profiles\abc`)
	if p.Code != ProfileStorage || !strings.Contains(p.Message, `C:\data\profiles\abc`) {
		t.Fatal(p)
	}
}
