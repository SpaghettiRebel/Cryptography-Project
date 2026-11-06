package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"cryptochat/server/internal/chats"
	"cryptochat/server/internal/keyexchange"
	"cryptochat/server/internal/store"
)

func TestToStatusMappings(t *testing.T) {
	for _, m := range errorMappings {
		err := toStatus(fmt.Errorf("обёртка: %w", m.target))
		if status.Code(err) != m.code || ReasonOf(err) != m.reason {
			t.Errorf("%v: код %v, причина %q", m.target, status.Code(err), ReasonOf(err))
		}
	}
}

func TestUnknownErrorHidesText(t *testing.T) {
	err := toStatus(errors.New("секретный текст драйвера"))
	if status.Code(err) != codes.Internal || strings.Contains(err.Error(), "секретный") {
		t.Fatalf("%v", err)
	}
	if toStatus(nil) != nil {
		t.Fatal("nil должен оставаться nil")
	}
}

func TestContextErrors(t *testing.T) {
	if c := status.Code(toStatus(context.Canceled)); c != codes.Canceled {
		t.Errorf("Canceled: %v", c)
	}
	if c := status.Code(toStatus(context.DeadlineExceeded)); c != codes.DeadlineExceeded {
		t.Errorf("DeadlineExceeded: %v", c)
	}
}

func TestChatErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		code   codes.Code
		reason string
	}{
		{store.ErrNotContact, codes.FailedPrecondition, ReasonNotContact},
		{store.ErrSelfChat, codes.InvalidArgument, ReasonSelfChat},
		{chats.ErrInvalidChatID, codes.InvalidArgument, ReasonInvalidChatID},
		{store.ErrChatNotFound, codes.NotFound, ReasonChatNotFound},
		{store.ErrChatClosed, codes.FailedPrecondition, ReasonChatClosed},
		{store.ErrNotConnected, codes.FailedPrecondition, ReasonNotConnected},
		{keyexchange.ErrInvalidPublicValue, codes.InvalidArgument, ReasonInvalidPublicValue},
		{store.ErrValueConflict, codes.AlreadyExists, ReasonValueAlreadyPublished},
	}
	for _, c := range cases {
		got := toStatus(fmt.Errorf("обёртка: %w", c.err))
		if status.Code(got) != c.code || ReasonOf(got) != c.reason {
			t.Errorf("%v: %v, %q", c.err, status.Code(got), ReasonOf(got))
		}
	}
	param := toStatus(chats.ParamError{Field: "key_bits"})
	if status.Code(param) != codes.InvalidArgument || ReasonOf(param) != ReasonInvalidChatParams || FieldOf(param) != "key_bits" {
		t.Errorf("параметр: %v, %q, %q", status.Code(param), ReasonOf(param), FieldOf(param))
	}
	// FR-021: текст ошибки базы наружу не передаётся.
	internal := toStatus(errors.New("postgres: P0001: значение-из-запроса"))
	if status.Code(internal) != codes.Internal || strings.Contains(internal.Error(), "значение-из-запроса") {
		t.Errorf("внутренняя ошибка: %v", internal)
	}
}
