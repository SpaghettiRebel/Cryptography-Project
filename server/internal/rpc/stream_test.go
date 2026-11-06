package rpc

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	apiv1 "cryptochat/server/api/v1"
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

func TestStreamInterceptorsLogAndRecover(t *testing.T) {
	const secret = "содержимое-фрагмента"
	var out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo}))
	info := &grpc.StreamServerInfo{FullMethod: "/cryptochat.v1.Delivery/ReceiveFragments"}
	chain := func(handler grpc.StreamHandler) error {
		return logStreamInterceptor(logger)(nil, fakeStream{ctx: bg}, info, func(srv any, ss grpc.ServerStream) error {
			return recoverStreamInterceptor(logger)(srv, ss, info, handler)
		})
	}

	err := chain(func(any, grpc.ServerStream) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	logged := out.String()
	for _, want := range []string{info.FullMethod, "code=OK", "duration="} {
		if !strings.Contains(logged, want) {
			t.Errorf("в журнале нет %q: %s", want, logged)
		}
	}

	out.Reset()
	err = chain(func(any, grpc.ServerStream) error { panic(secret) })
	if status.Code(err) != codes.Internal {
		t.Fatalf("паника: %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Error("значение паники в ответе")
	}
	logged = out.String()
	if !strings.Contains(logged, "паника") || !strings.Contains(logged, "code=Internal") {
		t.Errorf("журнал: %s", logged)
	}
	if strings.Contains(logged, secret) {
		t.Error("значение паники в журнале")
	}

	// Проверка готовности пишется уровнем отладки и при обычном уровне не видна.
	out.Reset()
	health := &grpc.StreamServerInfo{FullMethod: "/grpc.health.v1.Health/Watch"}
	if err := logStreamInterceptor(logger)(nil, fakeStream{ctx: bg}, health, func(any, grpc.ServerStream) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("журнал проверки готовности: %s", out.String())
	}
}

// Паника настоящего обработчика потока даёт Internal, и следующий запрос работает.
func TestStreamPanicThroughServer(t *testing.T) {
	h := start(t, nil, func(d *Deps) { d.Delivery = nil })
	token, _ := h.signUp(t, "alice")
	stream, err := apiv1.NewDeliveryClient(h.conn).ReceiveFragments(auth(token))
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&apiv1.ReceiveRequest{Kind: &apiv1.ReceiveRequest_Open_{Open: &apiv1.ReceiveRequest_Open{ChatId: make([]byte, 16)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.Internal {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(h.log.String(), "паника") {
		t.Error("паника не записана")
	}
	if _, err := h.contacts.ListContacts(auth(token), &apiv1.ListContactsRequest{}); err != nil {
		t.Fatalf("сервер не работает после паники: %v", err)
	}
}
