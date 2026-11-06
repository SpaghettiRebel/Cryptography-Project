package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"cryptochat/server/internal/store"
)

func TestMapError(t *testing.T) {
	ctx := context.Background()
	for _, code := range []string{"57P01", "57P02", "57P03", "08006", "53300"} {
		wrapped := fmt.Errorf("запрос: %w", &pgconn.PgError{Code: code})
		if err := mapError(ctx, wrapped); !errors.Is(err, store.ErrUnavailable) {
			t.Errorf("%s: %v", code, err)
		}
	}
	for _, code := range []string{"23505", "42P01", "22001"} {
		err := mapError(ctx, &pgconn.PgError{Code: code})
		if err == nil || errors.Is(err, store.ErrUnavailable) {
			t.Errorf("%s: %v", code, err)
		}
	}
	if err := mapError(ctx, errors.New("обрыв")); !errors.Is(err, store.ErrUnavailable) {
		t.Errorf("обрыв: %v", err)
	}
	if err := mapError(ctx, pgx.ErrNoRows); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("нет строк: %v", err)
	}
	if err := mapError(ctx, nil); err != nil {
		t.Errorf("nil: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := mapError(canceled, &pgconn.PgError{Code: "57P01"}); !errors.Is(err, context.Canceled) {
		t.Errorf("отмена: %v", err)
	}
}
