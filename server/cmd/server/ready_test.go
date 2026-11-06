package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cryptochat/server/internal/delivery"
)

func TestReadyAll(t *testing.T) {
	ok := func(context.Context) error { return nil }
	db := func(context.Context) error { return errors.New("база недоступна") }
	broker := func(context.Context) error { return delivery.ErrBrokerUnavailable }
	ctx := context.Background()
	if err := readyAll(ok, ok)(ctx); err != nil {
		t.Fatal(err)
	}
	if err := readyAll(db, ok)(ctx); err == nil {
		t.Error("отказ базы не замечен")
	}
	if err := readyAll(ok, broker)(ctx); !errors.Is(err, delivery.ErrBrokerUnavailable) {
		t.Errorf("отказ брокера: %v", err)
	}
	// Текст отказа брокера не содержит адреса с паролем.
	if err := readyAll(ok, broker)(ctx); strings.Contains(err.Error(), "amqp://") || strings.Contains(err.Error(), "rabbitmq:5672") {
		t.Errorf("адрес в тексте: %v", err)
	}
}
