package password

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	h := New()
	ctx := context.Background()
	a, err := h.Hash(ctx, "верный пароль 1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("формат: %s", a)
	}
	if strings.Contains(a, "верный") {
		t.Fatal("в строке есть пароль")
	}
	b, err := h.Hash(ctx, "верный пароль 1")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("два хеша одного пароля должны различаться")
	}
	_, salt, key, err := parse(a)
	if err != nil || len(salt) != saltLen || len(key) != keyLen {
		t.Fatalf("соль %d байт, ключ %d байт, %v", len(salt), len(key), err)
	}
	if err := h.Verify(ctx, "верный пароль 1", a); err != nil {
		t.Fatalf("верный пароль: %v", err)
	}
	if err := h.Verify(ctx, "неверный", a); !errors.Is(err, ErrMismatch) {
		t.Fatalf("неверный пароль: %v", err)
	}
}

func TestVerifyMalformed(t *testing.T) {
	h := New()
	ctx := context.Background()
	good, err := h.Hash(ctx, "пароль-пароль")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"", "мусор", "$argon2id$", "$argon2i$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",
		"$argon2id$v=18$m=65536,t=3,p=4$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=x,t=3,p=4$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=65536,t=0,p=4$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=4194304,t=3,p=4$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=65536,t=3,p=0$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=65536,t=3,p=4$!!!$aGFzaA",
		"$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$!!!",
		good[:len(good)-1] + "$лишнее",
	} {
		if err := h.Verify(ctx, "пароль-пароль", bad); !errors.Is(err, ErrMalformed) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// Параметры читаются из строки: хеш со старыми параметрами остаётся рабочим.
func TestVerifyUsesStoredParams(t *testing.T) {
	h := New()
	encoded := encode(params{time: 1, memory: 8 * 1024, threads: 1}, []byte("0123456789abcdef"), "пароль-пароль")
	if err := h.Verify(context.Background(), "пароль-пароль", encoded); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	h := New()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.Hash(context.Background(), "пароль-пароль"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := h.peak.Load(); got > maxConcurrent {
		t.Fatalf("одновременных вычислений %d, предел %d", got, maxConcurrent)
	}
}

func TestCanceledContext(t *testing.T) {
	h := New()
	for i := 0; i < maxConcurrent; i++ {
		h.slots <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Hash(ctx, "пароль-пароль"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Hash: %v", err)
	}
	if err := h.Verify(ctx, "пароль-пароль", "мусор"); !errors.Is(err, ErrMalformed) {
		t.Fatalf("разбор идёт до ожидания: %v", err)
	}
}
