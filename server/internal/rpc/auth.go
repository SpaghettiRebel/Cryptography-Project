package rpc

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	apiv1 "cryptochat/server/api/v1"
	"cryptochat/server/internal/accounts"
	"cryptochat/server/internal/store"
)

type identityKey struct{}

// identity — проверенный владелец сессии.
type identity struct {
	user  store.UserRef
	token string
}

// publicMethods не требуют сессии.
var publicMethods = map[string]bool{
	apiv1.Accounts_Register_FullMethodName: true,
	apiv1.Accounts_Login_FullMethodName:    true,
}

func tokenFrom(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get("authorization")
	if len(values) != 1 {
		return ""
	}
	token, ok := strings.CutPrefix(values[0], "Bearer ")
	if !ok {
		return ""
	}
	return token
}

// authInterceptor проверяет сессию; пользователь берётся только из неё, а не из тела запроса.
func authInterceptor(svc *accounts.Service) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if publicMethods[info.FullMethod] || strings.HasPrefix(info.FullMethod, "/grpc.health.v1.Health/") {
			return handler(ctx, req)
		}
		token := tokenFrom(ctx)
		user, err := svc.Authenticate(ctx, token)
		if err != nil {
			return nil, toStatus(err)
		}
		return handler(context.WithValue(ctx, identityKey{}, identity{user: user, token: token}), req)
	}
}

type sessionStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s sessionStream) Context() context.Context { return s.ctx }

// authStreamInterceptor применяет те же правила к потоковым вызовам: новый поток без сессии закрыт
// по умолчанию.
func authStreamInterceptor(svc *accounts.Service) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if strings.HasPrefix(info.FullMethod, "/grpc.health.v1.Health/") {
			return handler(srv, ss)
		}
		token := tokenFrom(ss.Context())
		user, err := svc.Authenticate(ss.Context(), token)
		if err != nil {
			return toStatus(err)
		}
		ctx := context.WithValue(ss.Context(), identityKey{}, identity{user: user, token: token})
		return handler(srv, sessionStream{ServerStream: ss, ctx: ctx})
	}
}

func identityFrom(ctx context.Context) identity {
	id, ok := ctx.Value(identityKey{}).(identity)
	if !ok {
		panic("rpc: обработчик вызван без проверки сессии")
	}
	return id
}
