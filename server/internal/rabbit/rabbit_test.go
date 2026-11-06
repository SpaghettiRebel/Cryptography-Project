package rabbit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cryptochat/server/internal/delivery"
	"cryptochat/server/internal/delivery/brokertest"
)

// brokerURL возвращает адрес тестового брокера. Без переменной тест пропускается, а при
// CHAT_REQUIRE_RABBITMQ=1 падает: так проверку нельзя пропустить незаметно.
func brokerURL(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("нужен RabbitMQ")
	}
	raw := os.Getenv("CHAT_TEST_RABBITMQ_URL")
	if raw == "" {
		if os.Getenv("CHAT_REQUIRE_RABBITMQ") == "1" {
			t.Fatal("CHAT_TEST_RABBITMQ_URL не задан, а CHAT_REQUIRE_RABBITMQ=1")
		}
		t.Skip("CHAT_TEST_RABBITMQ_URL не задан")
	}
	return raw
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// running запускает брокер и останавливает его в конце проверки.
func running(t *testing.T, raw string, logger *slog.Logger, limits Limits) *Broker {
	t.Helper()
	b := New(raw, logger, limits)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		b.Close()
	})
	return b
}

func waitReady(t *testing.T, b *Broker, limit time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	for b.Ready() != nil {
		if time.Since(start) > limit {
			t.Fatalf("брокер не стал готов за %v", limit)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return time.Since(start)
}

func TestBrokerRules(t *testing.T) {
	raw := brokerURL(t)
	brokertest.Run(t, func(t *testing.T, maxMessages int) delivery.Broker {
		b := running(t, raw, nil, Limits{MaxLength: int64(maxMessages)})
		waitReady(t, b, 60*time.Second)
		return b
	})
}

// proxy — TCP-посредник, который может разорвать все соединения и перестать принимать новые.
type proxy struct {
	lis    net.Listener
	target string

	mu    sync.Mutex
	open  bool
	conns map[net.Conn]struct{}
}

func startProxy(t *testing.T, target string) *proxy {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{lis: lis, target: target, open: true, conns: map[net.Conn]struct{}{}}
	go p.accept()
	t.Cleanup(func() {
		_ = lis.Close()
		p.set(false)
	})
	return p
}

func (p *proxy) accept() {
	for {
		c, err := p.lis.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		open := p.open
		p.mu.Unlock()
		if !open {
			_ = c.Close()
			continue
		}
		up, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = c.Close()
			continue
		}
		p.mu.Lock()
		p.conns[c], p.conns[up] = struct{}{}, struct{}{}
		p.mu.Unlock()
		go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
		go func() { _, _ = io.Copy(c, up); _ = c.Close() }()
	}
}

// set открывает посредника или разрывает все соединения и закрывает его.
func (p *proxy) set(open bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.open = open
	if !open {
		for c := range p.conns {
			_ = c.Close()
		}
		p.conns = map[net.Conn]struct{}{}
	}
}

func viaProxy(t *testing.T, raw string, p *proxy) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	u.Host = p.lis.Addr().String()
	return u.String()
}

func targetOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func TestStartWithoutBroker(t *testing.T) {
	raw := brokerURL(t)
	p := startProxy(t, targetOf(t, raw))
	p.set(false)
	b := running(t, viaProxy(t, raw, p), nil, Limits{})
	time.Sleep(300 * time.Millisecond)
	if err := b.Ready(); !errors.Is(err, delivery.ErrBrokerUnavailable) {
		t.Fatalf("Ready без брокера: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Publish(ctx, "q", []byte("x")); !errors.Is(err, delivery.ErrBrokerUnavailable) {
		t.Fatalf("Publish без брокера: %v", err)
	}
	p.set(true)
	if took := waitReady(t, b, 10*time.Second); took > 10*time.Second {
		t.Fatalf("брокер появился, готовность за %v", took)
	}
}

func TestConnectionLoss(t *testing.T) {
	raw := brokerURL(t)
	p := startProxy(t, targetOf(t, raw))
	log := &syncBuffer{}
	b := running(t, viaProxy(t, raw, p), slog.New(slog.NewTextHandler(log, nil)), Limits{})
	waitReady(t, b, 60*time.Second)
	ctx := context.Background()
	queue := "test.loss." + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	if err := b.EnsureQueue(ctx, queue); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := New(raw, nil, Limits{})
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go cleanup.Run(cctx)
		for cleanup.Ready() != nil {
			time.Sleep(20 * time.Millisecond)
		}
		_ = cleanup.DeleteQueue(ctx, queue)
		cleanup.Close()
	})
	if err := b.Publish(ctx, queue, []byte("ожидает")); err != nil {
		t.Fatal(err)
	}
	consumer, err := b.Consume(ctx, queue, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	if d := <-consumer.Deliveries(); string(d.Body) != "ожидает" {
		t.Fatalf("тело %q", d.Body)
	}

	p.set(false)
	// Потребитель завершается той же причиной.
	select {
	case _, ok := <-consumer.Deliveries():
		if ok {
			t.Fatal("лишняя выдача")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("потребитель не завершился")
	}
	if !errors.Is(consumer.Err(), delivery.ErrBrokerUnavailable) {
		t.Fatalf("причина: %v", consumer.Err())
	}
	deadline := time.Now().Add(5 * time.Second)
	for b.Ready() == nil {
		if time.Now().After(deadline) {
			t.Fatal("Ready не заметил разрыва")
		}
		time.Sleep(20 * time.Millisecond)
	}
	start := time.Now()
	pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := b.Publish(pubCtx, queue, []byte("x")); !errors.Is(err, delivery.ErrBrokerUnavailable) {
		t.Fatalf("Publish при разрыве: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Publish занял %v", took)
	}

	p.set(true)
	waitReady(t, b, 10*time.Second)
	// Сообщение, не подтверждённое до разрыва, доступно после восстановления.
	again, err := b.Consume(ctx, queue, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	select {
	case d := <-again.Deliveries():
		if string(d.Body) != "ожидает" || !d.Redelivered {
			t.Fatalf("после восстановления: %q, %v", d.Body, d.Redelivered)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("сообщение не вернулось")
	}
	// Издатель работает на новых каналах.
	for i := 0; i < publisherChannels+1; i++ {
		if err := b.Publish(ctx, queue, []byte("новое")); err != nil {
			t.Fatalf("Publish после восстановления: %v", err)
		}
	}

	logged := log.String()
	if !strings.Contains(logged, "потеряно") {
		t.Errorf("в журнале нет сбоя: %s", logged)
	}
	password := "test"
	if u, err := url.Parse(raw); err == nil {
		password, _ = u.User.Password()
	}
	if strings.Contains(logged, "amqp://") || strings.Contains(logged, ":"+password+"@") {
		t.Errorf("в журнале адрес с паролем: %s", logged)
	}
}
