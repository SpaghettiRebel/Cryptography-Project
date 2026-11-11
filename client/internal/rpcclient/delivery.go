package rpcclient

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc/metadata"

	apiv1 "cryptochat/client/internal/api/v1"
	"cryptochat/client/internal/apperr"
)

// Сроки доставки: публикация фрагмента и ожидание ответа opened на открытие потока.
const (
	SendTimeout = 10 * time.Second
	OpenTimeout = 10 * time.Second
	// ReceivePrefetch — сколько неподтверждённых фрагментов сервер выдаёт в поток.
	ReceivePrefetch = 16
)

// Delivery — фрагмент, выданный потоком приёма. ID действует только в этом потоке.
type Delivery struct {
	ID          uint64
	Wire        []byte
	Redelivered bool
}

// ReceiveStream — открытый поток приёма чата. Recv и Ack можно вызывать из двух разных горутин
// одновременно; Close закрывает поток без подтверждения невыданного и неподтверждённого.
type ReceiveStream interface {
	Recv() (Delivery, error)
	Ack(id uint64) error
	Close() error
}

// SendFragment передаёт фрагмент чата на сервер со сроком SendTimeout.
func (c *Conn) SendFragment(ctx context.Context, chatID [16]byte, wire []byte) error {
	_, err := call(c, ctx, SendTimeout, func(ctx context.Context) (*apiv1.SendFragmentResponse, error) {
		return c.delivery.SendFragment(ctx, &apiv1.SendFragmentRequest{ChatId: chatID[:], Fragment: wire})
	})
	return err
}

type receiveStream struct {
	conn   *Conn
	stream apiv1.Delivery_ReceiveFragmentsClient
	cancel context.CancelFunc

	sendMu sync.Mutex
}

// OpenReceive открывает поток приёма: посылает open и ждёт opened не дольше OpenTimeout.
func (c *Conn) OpenReceive(ctx context.Context, chatID [16]byte) (ReceiveStream, error) {
	c.mu.RLock()
	token := c.token
	c.mu.RUnlock()
	ctx, cancel := context.WithCancel(ctx)
	if token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	}
	timer := time.AfterFunc(OpenTimeout, cancel)
	fail := func(err error) (ReceiveStream, error) {
		expired := !timer.Stop()
		cancel()
		if expired {
			return nil, apperr.New(apperr.Timeout)
		}
		return nil, c.Classify(err)
	}
	st, err := c.delivery.ReceiveFragments(ctx)
	if err != nil {
		return fail(err)
	}
	if err := st.Send(&apiv1.ReceiveRequest{Kind: &apiv1.ReceiveRequest_Open_{Open: &apiv1.ReceiveRequest_Open{ChatId: chatID[:]}}}); err != nil {
		// Причину отказа несёт статус потока, а не ошибка отправки.
		_, err = st.Recv()
		return fail(err)
	}
	ev, err := st.Recv()
	if err != nil {
		return fail(err)
	}
	if ev.GetOpened() == nil {
		timer.Stop()
		cancel()
		return nil, apperr.New(apperr.Internal)
	}
	if !timer.Stop() {
		cancel()
		return nil, apperr.New(apperr.Timeout)
	}
	return &receiveStream{conn: c, stream: st, cancel: cancel}, nil
}

// Recv ждёт следующий фрагмент. Завершение потока сервером возвращается ошибкой клиента по причине.
func (s *receiveStream) Recv() (Delivery, error) {
	for {
		ev, err := s.stream.Recv()
		if err != nil {
			return Delivery{}, s.conn.Classify(err)
		}
		if f := ev.GetFragment(); f != nil {
			return Delivery{ID: f.GetDeliveryId(), Wire: f.GetFragment(), Redelivered: f.GetRedelivered()}, nil
		}
	}
}

// Ack подтверждает фрагмент после его надёжной записи.
func (s *receiveStream) Ack(id uint64) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if err := s.stream.Send(&apiv1.ReceiveRequest{Kind: &apiv1.ReceiveRequest_Ack_{Ack: &apiv1.ReceiveRequest_Ack{DeliveryId: id}}}); err != nil {
		return apperr.FromStatus(err)
	}
	return nil
}

// Close отменяет поток: сервер вернёт неподтверждённые фрагменты в очередь.
func (s *receiveStream) Close() error {
	s.cancel()
	return nil
}
