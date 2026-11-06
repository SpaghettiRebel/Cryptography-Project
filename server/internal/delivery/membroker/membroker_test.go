package membroker_test

import (
	"context"
	"errors"
	"testing"

	"cryptochat/server/internal/delivery"
	"cryptochat/server/internal/delivery/brokertest"
	"cryptochat/server/internal/delivery/membroker"
)

func TestBrokerRules(t *testing.T) {
	brokertest.Run(t, func(_ *testing.T, maxMessages int) delivery.Broker { return membroker.New(maxMessages) })
}

// Сбой завершает потребителей, а принятые сообщения остаются.
func TestSetDown(t *testing.T) {
	ctx := context.Background()
	b := membroker.New(0)
	if err := b.EnsureQueue(ctx, "q"); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(ctx, "q", []byte("x")); err != nil {
		t.Fatal(err)
	}
	c, err := b.Consume(ctx, "q", 1)
	if err != nil {
		t.Fatal(err)
	}
	b.SetDown(true)
	if _, ok := <-c.Deliveries(); ok {
		t.Fatal("выдача после сбоя")
	}
	if !errors.Is(c.Err(), delivery.ErrBrokerUnavailable) {
		t.Fatalf("причина: %v", c.Err())
	}
	for name, err := range map[string]error{
		"Ready":       b.Ready(),
		"EnsureQueue": b.EnsureQueue(ctx, "q"),
		"Publish":     b.Publish(ctx, "q", []byte("y")),
		"DeleteQueue": b.DeleteQueue(ctx, "q"),
	} {
		if !errors.Is(err, delivery.ErrBrokerUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	b.SetDown(false)
	again, err := b.Consume(ctx, "q", 1)
	if err != nil {
		t.Fatal(err)
	}
	if d := <-again.Deliveries(); string(d.Body) != "x" || !d.Redelivered {
		t.Fatalf("после возврата: %q, %v", d.Body, d.Redelivered)
	}
}
