// Пакет rpc содержит gRPC-слой сервера: обработчики, авторизацию, журнал и преобразование ошибок.
package rpc

import (
	"context"
	"crypto/tls"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	apiv1 "cryptochat/server/api/v1"
	"cryptochat/server/internal/accounts"
	"cryptochat/server/internal/contacts"
)

// MaxMessageSize ограничивает размер запроса.
const MaxMessageSize = 64 * 1024

// Deps — зависимости gRPC-слоя.
type Deps struct {
	Accounts *accounts.Service
	Contacts *contacts.Service
	Logger   *slog.Logger
	// Ready проверяет готовность хранилища.
	Ready func(context.Context) error
}

// Server объединяет gRPC-сервер и проверку готовности.
type Server struct {
	GRPC   *grpc.Server
	health *health.Server
	ready  func(context.Context) error
	logger *slog.Logger
}

// NewServer создаёт сервер, принимающий только TLS.
func NewServer(tlsConfig *tls.Config, deps Deps) *Server {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		GRPC: grpc.NewServer(
			grpc.Creds(credentials.NewTLS(tlsConfig)),
			grpc.MaxRecvMsgSize(MaxMessageSize),
			grpc.ChainStreamInterceptor(authStreamInterceptor(deps.Accounts)),
			grpc.ChainUnaryInterceptor(
				logInterceptor(logger),
				recoverInterceptor(logger),
				authInterceptor(deps.Accounts),
			),
		),
		health: health.NewServer(),
		ready:  deps.Ready,
		logger: logger,
	}
	s.health.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	healthpb.RegisterHealthServer(s.GRPC, s.health)
	apiv1.RegisterAccountsServer(s.GRPC, &accountsHandler{svc: deps.Accounts})
	apiv1.RegisterContactsServer(s.GRPC, &contactsHandler{svc: deps.Contacts})
	return s
}

// CheckReady обновляет состояние по результату проверки хранилища.
func (s *Server) CheckReady(ctx context.Context) {
	state := healthpb.HealthCheckResponse_SERVING
	if s.ready != nil {
		if err := s.ready(ctx); err != nil {
			state = healthpb.HealthCheckResponse_NOT_SERVING
			s.logger.Warn("хранилище недоступно", "err", err)
		}
	}
	s.health.SetServingStatus("", state)
}

// RunHealth проверяет готовность раз в 2 секунды, пока ctx не отменён.
func (s *Server) RunHealth(ctx context.Context) {
	s.CheckReady(ctx)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.CheckReady(ctx)
		}
	}
}

// Shutdown переводит проверку готовности в состояние остановки.
func (s *Server) Shutdown() { s.health.Shutdown() }

// logInterceptor пишет метод, код и длительность. Тела запросов и метаданные в журнал не попадают.
func logInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		code := status.Code(err)
		level := slog.LevelInfo
		if strings.HasPrefix(info.FullMethod, "/grpc.health.v1.Health/") {
			level = slog.LevelDebug
		}
		if code == codes.Internal || code == codes.Unavailable {
			level = slog.LevelError
		}
		logger.Log(ctx, level, "запрос", "method", info.FullMethod, "code", code.String(), "duration", time.Since(start))
		return resp, err
	}
}

// recoverInterceptor не даёт панике обработчика завершить сервер. Значение паники и стек не пишутся
// целиком: значение может содержать данные запроса.
func recoverInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("паника в обработчике", "method", info.FullMethod, "stack", stackFrames())
				resp, err = nil, status.Error(codes.Internal, "внутренняя ошибка")
			}
		}()
		return handler(ctx, req)
	}
}

// stackFrames возвращает стек: имена функций, адреса и аргументы в шестнадцатеричном виде, но не
// содержимое строк.
func stackFrames() string { return string(debug.Stack()) }
