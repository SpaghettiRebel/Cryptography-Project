package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
