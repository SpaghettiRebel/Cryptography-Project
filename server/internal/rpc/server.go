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
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	apiv1 "cryptochat/server/api/v1"
	"cryptochat/server/internal/accounts"
	"cryptochat/server/internal/chats"
	"cryptochat/server/internal/contacts"
	"cryptochat/server/internal/delivery"
	"cryptochat/server/internal/keyexchange"
)

// MaxMessageSize ограничивает размер запроса.
const MaxMessageSize = 64 * 1024

// Deps — зависимости gRPC-слоя.
type Deps struct {
	Accounts    *accounts.Service
	Contacts    *contacts.Service
	Chats       *chats.Service
	KeyExchange *keyexchange.Service
	Delivery    *delivery.Service
	Logger      *slog.Logger
	// Ready проверяет готовность зависимостей сервера.
	Ready func(context.Context) error
	// OpenTimeout — срок ожидания первого сообщения потока; нулевое значение означает 10 с.
	OpenTimeout time.Duration
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
			// Полуоткрытое соединение обнаруживается не дольше чем за 40 с.
			grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
			grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 20 * time.Second, PermitWithoutStream: true}),
			grpc.ChainStreamInterceptor(
				logStreamInterceptor(logger),
				recoverStreamInterceptor(logger),
				authStreamInterceptor(deps.Accounts),
			),
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
	apiv1.RegisterChatsServer(s.GRPC, &chatsHandler{svc: deps.Chats})
	apiv1.RegisterKeyExchangeServer(s.GRPC, &keyExchangeHandler{svc: deps.KeyExchange})
	apiv1.RegisterDeliveryServer(s.GRPC, &deliveryHandler{svc: deps.Delivery, accounts: deps.Accounts, logger: logger, openTimeout: deps.OpenTimeout})
	return s
}

// CheckReady обновляет состояние по результату проверки зависимостей. Текст ошибки в журнал не попадает:
// он может содержать адрес зависимости.
func (s *Server) CheckReady(ctx context.Context) {
	state := healthpb.HealthCheckResponse_SERVING
	if s.ready != nil {
		if err := s.ready(ctx); err != nil {
			state = healthpb.HealthCheckResponse_NOT_SERVING
			s.logger.Warn("сервер не готов: зависимость недоступна")
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

// logCall пишет метод, код и длительность. Тела запросов и метаданные в журнал не попадают.
func logCall(ctx context.Context, logger *slog.Logger, method string, err error, start time.Time) {
	code := status.Code(err)
	level := slog.LevelInfo
	if strings.HasPrefix(method, "/grpc.health.v1.Health/") {
		level = slog.LevelDebug
	}
	if code == codes.Internal || code == codes.Unavailable {
		level = slog.LevelError
	}
	logger.Log(ctx, level, "запрос", "method", method, "code", code.String(), "duration", time.Since(start))
}

func logInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		logCall(ctx, logger, info.FullMethod, err, start)
		return resp, err
	}
}

// logStreamInterceptor пишет те же сведения по завершении потока; контекст потока к этому времени мог
// быть отменён, поэтому запись идёт с фоновым.
func logStreamInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		logCall(context.Background(), logger, info.FullMethod, err, start)
		return err
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

// recoverStreamInterceptor применяет то же правило к потоковым вызовам.
func recoverStreamInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("паника в обработчике", "method", info.FullMethod, "stack", stackFrames())
				err = status.Error(codes.Internal, "внутренняя ошибка")
			}
		}()
		return handler(srv, ss)
	}
}

// stackFrames возвращает стек: имена функций, адреса и аргументы в шестнадцатеричном виде, но не
// содержимое строк.
func stackFrames() string { return string(debug.Stack()) }
