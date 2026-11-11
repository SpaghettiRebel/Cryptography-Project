package rpcclient

import (
	"context"
	"crypto/tls"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	apiv1 "cryptochat/client/internal/api/v1"
	"cryptochat/client/internal/apperr"
)

type fakeDelivery struct {
	apiv1.UnimplementedDeliveryServer
	mu       sync.Mutex
	auth     string
	opened   []byte
	acks     []uint64
	deadline time.Duration
	reason   string // причина завершения потока после двух подтверждений
	silent   bool   // не отвечать opened
}

func (f *fakeDelivery) SendFragment(ctx context.Context, r *apiv1.SendFragmentRequest) (*apiv1.SendFragmentResponse, error) {
	if d, ok := ctx.Deadline(); ok {
		f.mu.Lock()
		f.deadline = time.Until(d)
		f.mu.Unlock()
	}
	if string(r.GetFragment()) == "full" {
		st, _ := status.New(codes.ResourceExhausted, "x").WithDetails(&errdetails.ErrorInfo{Reason: "QUEUE_FULL", Domain: "cryptochat"})
		return nil, st.Err()
	}
	return &apiv1.SendFragmentResponse{}, nil
}

func (f *fakeDelivery) ReceiveFragments(st apiv1.Delivery_ReceiveFragmentsServer) error {
	md, _ := metadata.FromIncomingContext(st.Context())
	req, err := st.Recv()
	if err != nil {
		return err
	}
	f.mu.Lock()
	if v := md.Get("authorization"); len(v) > 0 {
		f.auth = v[0]
	}
	f.opened = req.GetOpen().GetChatId()
	silent, reason := f.silent, f.reason
	f.mu.Unlock()
	if silent {
		<-st.Context().Done()
		return st.Context().Err()
	}
	if err := st.Send(&apiv1.ReceiveEvent{Kind: &apiv1.ReceiveEvent_Opened_{Opened: &apiv1.ReceiveEvent_Opened{}}}); err != nil {
		return err
	}
	for i := uint64(1); i <= 2; i++ {
		if err := st.Send(&apiv1.ReceiveEvent{Kind: &apiv1.ReceiveEvent_Fragment{Fragment: &apiv1.FragmentDelivery{
			DeliveryId: i, Fragment: []byte{byte(i)}, Redelivered: i == 2}}}); err != nil {
			return err
		}
	}
	for n := 0; n < 2; n++ {
		req, err := st.Recv()
		if err != nil {
			return err
		}
		f.mu.Lock()
		f.acks = append(f.acks, req.GetAck().GetDeliveryId())
		f.mu.Unlock()
	}
	st2, _ := status.New(codes.Aborted, "x").WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: "cryptochat"})
	return st2.Err()
}

func startDelivery(t *testing.T, f *fakeDelivery) *Conn {
	t.Helper()
	pair, cert := selfSigned(t, "localhost", "127.0.0.1")
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13})))
	apiv1.RegisterDeliveryServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	_, port, _ := net.SplitHostPort(lis.Addr().String())
	return dial(t, "localhost:"+port, cert)
}

func TestReceiveStream(t *testing.T) {
	f := &fakeDelivery{reason: "STREAM_REPLACED"}
	c := startDelivery(t, f)
	c.SetToken("tok")
	id := [16]byte{1, 2, 3}
	s, err := c.OpenReceive(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f.mu.Lock()
	if f.auth != "Bearer tok" || string(f.opened) != string(id[:]) {
		t.Fatalf("заголовок %q, чат %x", f.auth, f.opened)
	}
	f.mu.Unlock()
	// Чтение и подтверждение из двух разных горутин.
	got := make(chan Delivery, 2)
	errs := make(chan error, 1)
	go func() {
		for {
			d, err := s.Recv()
			if err != nil {
				errs <- err
				return
			}
			got <- d
		}
	}()
	for i := 0; i < 2; i++ {
		d := <-got
		if d.ID != uint64(i+1) || d.Wire[0] != byte(i+1) || d.Redelivered != (i == 1) {
			t.Fatalf("выдача %+v", d)
		}
		if err := s.Ack(d.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-errs; apperr.CodeOf(err) != apperr.StreamReplaced {
		t.Fatalf("завершение потока: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.acks) != 2 || f.acks[0] != 1 || f.acks[1] != 2 {
		t.Fatalf("подтверждения %v", f.acks)
	}
}

func TestSendFragment(t *testing.T) {
	f := &fakeDelivery{}
	c := startDelivery(t, f)
	if err := c.SendFragment(context.Background(), [16]byte{1}, []byte("data")); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	d := f.deadline
	f.mu.Unlock()
	if d > SendTimeout || d < SendTimeout-time.Second {
		t.Fatalf("срок SendFragment %v", d)
	}
	if err := c.SendFragment(context.Background(), [16]byte{1}, []byte("full")); apperr.CodeOf(err) != apperr.QueueFull {
		t.Fatalf("QUEUE_FULL: %v", err)
	}
}

func TestOpenTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("ждёт срок открытия 10 с")
	}
	f := &fakeDelivery{silent: true}
	c := startDelivery(t, f)
	start := time.Now()
	if _, err := c.OpenReceive(context.Background(), [16]byte{1}); apperr.CodeOf(err) != apperr.Timeout {
		t.Fatalf("без opened: %v", err)
	}
	if d := time.Since(start); d < OpenTimeout-time.Second || d > OpenTimeout+3*time.Second {
		t.Fatalf("ожидание %v", d)
	}
}
