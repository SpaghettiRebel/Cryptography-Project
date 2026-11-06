// Консольный сервер курсового проекта.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"cryptochat/server/internal/accounts"
	"cryptochat/server/internal/bootstrap"
	"cryptochat/server/internal/chats"
	"cryptochat/server/internal/contacts"
	"cryptochat/server/internal/delivery"
	"cryptochat/server/internal/keyexchange"
	"cryptochat/server/internal/postgres"
	"cryptochat/server/internal/rabbit"
	"cryptochat/server/internal/rpc"
)

const (
	startupTimeout = 60 * time.Second
	stopTimeout    = 10 * time.Second
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	command := ""
	if len(args) > 0 {
		command = args[0]
	}
	switch command {
	case "":
		return serve(cfg)
	case "init":
		return bootstrap.Init(cfg.SecretsDir, cfg.CertsDir, cfg.SecretsOwner)
	case "healthcheck":
		return healthcheck(cfg)
	}
	return fmt.Errorf("неизвестная команда %q", command)
}

func serve(cfg config) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dsn, err := cfg.databaseURL(os.ReadFile)
	if err != nil {
		return err
	}
	brokerURL, err := cfg.brokerURL(os.ReadFile)
	if err != nil {
		return err
	}
	tlsConfig, err := bootstrap.ServerTLS(cfg.TLSCert, cfg.TLSKey)
	if err != nil {
		return err
	}
	db, err := postgres.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := waitForDB(ctx, db, logger); err != nil {
		return err
	}
	if err := db.Migrate(ctx); err != nil {
		return err
	}
	acc, err := accounts.New(ctx, db)
	if err != nil {
		return err
	}
	// Запуск брокера не ждёт: сервер работает и без него, а готовым становится при открытом соединении.
	broker := rabbit.New(brokerURL, logger, rabbit.Limits{})
	brokerCtx, stopBroker := context.WithCancel(context.Background())
	defer stopBroker()
	brokerDone := make(chan struct{})
	go func() {
		defer close(brokerDone)
		broker.Run(brokerCtx)
	}()
	deliverySvc := delivery.New(db, db, broker, logger, delivery.Options{})
	srv := rpc.NewServer(tlsConfig, rpc.Deps{
		Accounts:    acc,
		Contacts:    contacts.New(db),
		Chats:       chats.New(db),
		KeyExchange: keyexchange.New(db),
		Delivery:    deliverySvc,
		Logger:      logger,
		Ready:       readyAll(db.Ping, func(context.Context) error { return broker.Ready() }),
	})

	lis, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("адрес %s: %w", cfg.Listen, err)
	}
	healthCtx, stopHealth := context.WithCancel(ctx)
	defer stopHealth()
	go srv.RunHealth(healthCtx)
	cleanupCtx, stopCleanup := context.WithCancel(context.Background())
	defer stopCleanup()
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		deliverySvc.RunCleanup(cleanupCtx)
	}()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.GRPC.Serve(lis) }()
	logger.Info("сервер запущен", "addr", cfg.Listen)

	select {
	case err := <-serveErr:
		return fmt.Errorf("сервер остановился: %w", err)
	case <-ctx.Done():
	}
	logger.Info("остановка")
	stopHealth()
	srv.Shutdown()
	// Потоки завершаются раньше GracefulStop, иначе он ждал бы их весь предел.
	deliverySvc.Shutdown()
	stopped := make(chan struct{})
	go func() {
		srv.GRPC.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(stopTimeout):
		srv.GRPC.Stop()
	}
	stopCleanup()
	<-cleanupDone
	stopBroker()
	<-brokerDone
	broker.Close()
	logger.Info("сервер остановлен")
	return nil
}

func waitForDB(ctx context.Context, db *postgres.DB, logger *slog.Logger) error {
	deadline := time.Now().Add(startupTimeout)
	for {
		err := db.Ping(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return errors.New("база недоступна при запуске")
		}
		logger.Info("ожидание базы")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func healthcheck(cfg config) error {
	tlsConfig, err := bootstrap.ClientTLS(cfg.TLSCert, "localhost")
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(cfg.HealthAddr, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		return err
	}
	if r.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		return fmt.Errorf("состояние %s", r.GetStatus())
	}
	return nil
}
