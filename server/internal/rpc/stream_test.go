package rpc

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"

	"cryptochat/server/internal/accounts"
	"cryptochat/server/internal/store/memstore"
)

type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s fakeStream) Context() context.Context { return s.ctx }

func TestStreamRequiresSession(t *testing.T) {
	svc, err := accounts.New(bg, memstore.New())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register(bg, "alice", "password-1"); err != nil {
		t.Fatal(err)
	}
	session, err := svc.Login(bg, "alice", "password-1")
	if err != nil {
		t.Fatal(err)
	}
	interceptor := authStreamInterceptor(svc)
	call := func(ctx context.Context, method string) (string, bool, error) {
		var login string
		called := false
		err := interceptor(nil, fakeStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: method},
			func(_ any, ss grpc.ServerStream) error {
				called = true
				if id, ok := ss.Context().Value(identityKey{}).(identity); ok {
					login = id.user.Login
				}
				return nil
			})
		return login, called, err
	}

	_, called, err := call(bg, "/cryptochat.v1.Future/Watch")
	expectError(t, err, codes.Unauthenticated, ReasonUnauthenticated)
	if called {
		t.Error("обработчик вызван без сессии")
	}
	withToken := metadata.NewIncomingContext(bg, metadata.Pairs("authorization", "Bearer "+session.Token))
	login, called, err := call(withToken, "/cryptochat.v1.Future/Watch")
	if err != nil || !called || login != "alice" {
		t.Errorf("с сессией: %q, %v, %v", login, called, err)
	}
	if _, called, err := call(bg, "/grpc.health.v1.Health/Watch"); err != nil || !called {
		t.Errorf("проверка готовности: %v, %v", called, err)
	}
}
