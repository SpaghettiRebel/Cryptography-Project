package rpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apiv1 "cryptochat/server/api/v1"
	"cryptochat/server/internal/accounts"
	"cryptochat/server/internal/delivery"
)

const defaultOpenTimeout = 10 * time.Second

var errStreamInternal = errors.New("rpc: внутренняя ошибка потока")

type deliveryHandler struct {
	apiv1.UnimplementedDeliveryServer
	svc         *delivery.Service
	accounts    *accounts.Service
	logger      *slog.Logger
	openTimeout time.Duration
}

// SendFragment передаёт фрагмент без разбора.
func (h *deliveryHandler) SendFragment(ctx context.Context, req *apiv1.SendFragmentRequest) (*apiv1.SendFragmentResponse, error) {
	if err := h.svc.Send(ctx, identityFrom(ctx).user.ID, req.GetChatId(), req.GetFragment()); err != nil {
		return nil, toStatus(err)
	}
	return &apiv1.SendFragmentResponse{}, nil
}

type received struct {
	msg *apiv1.ReceiveRequest
	err error
}

// readStream читает сообщения клиента, пока поток жив. Перехватчик не видит собственных горутин обработчика,
// поэтому паника обрабатывается здесь же.
func (h *deliveryHandler) readStream(ctx context.Context, stream apiv1.Delivery_ReceiveFragmentsServer, out chan<- received) {
	defer func() {
		if r := recover(); r != nil {
			h.logger.Error("паника при чтении потока", "stack", stackFrames())
			select {
			case out <- received{err: errStreamInternal}:
			case <-ctx.Done():
			}
		}
	}()
	for {
		msg, err := stream.Recv()
		select {
		case out <- received{msg: msg, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

// writeStream пишет ответ на открытие и фрагменты. Тела фрагментов нигде не сохраняются.
func (h *deliveryHandler) writeStream(stream apiv1.Delivery_ReceiveFragmentsServer, sub *delivery.Subscription, done chan<- error) {
	defer func() {
		if r := recover(); r != nil {
			h.logger.Error("паника при записи потока", "stack", stackFrames())
			done <- errStreamInternal
		}
	}()
	opened := &apiv1.ReceiveEvent{Kind: &apiv1.ReceiveEvent_Opened_{Opened: &apiv1.ReceiveEvent_Opened{}}}
	if err := stream.Send(opened); err != nil {
		done <- err
		return
	}
	for d := range sub.Deliveries() {
		event := &apiv1.ReceiveEvent{Kind: &apiv1.ReceiveEvent_Fragment{Fragment: &apiv1.FragmentDelivery{
			DeliveryId: d.ID, Fragment: d.Body, Redelivered: d.Redelivered,
		}}}
		if err := stream.Send(event); err != nil {
			done <- err
			return
		}
	}
}

// clientEnded завершает поток по ошибке чтения или записи: клиент ушёл либо нарушил транспорт.
func clientEnded(sub *delivery.Subscription, err error) error {
	if errors.Is(err, errStreamInternal) {
		sub.Close(err)
		return status.Error(codes.Internal, "внутренняя ошибка")
	}
	sub.Close(err)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if st, ok := status.FromError(err); ok && st.Code() != codes.OK {
		return st.Err()
	}
	return toStatus(context.Canceled)
}

// ReceiveFragments обслуживает поток. Статус формирует только обработчик: заблокированную запись клиенту,
// который не читает, можно прервать только завершением обработчика.
func (h *deliveryHandler) ReceiveFragments(stream apiv1.Delivery_ReceiveFragmentsServer) error {
	ctx := stream.Context()
	who := identityFrom(ctx)
	messages := make(chan received)
	go h.readStream(ctx, stream, messages)

	openTimeout := h.openTimeout
	if openTimeout == 0 {
		openTimeout = defaultOpenTimeout
	}
	timer := time.NewTimer(openTimeout)
	defer timer.Stop()
	var chatID []byte
	select {
	case first := <-messages:
		if errors.Is(first.err, errStreamInternal) {
			return status.Error(codes.Internal, "внутренняя ошибка")
		}
		// Поток без открытия подключение не меняет.
		if first.err != nil || first.msg.GetOpen() == nil {
			return toStatus(delivery.ErrInvalidStreamRequest)
		}
		chatID = first.msg.GetOpen().GetChatId()
	case <-timer.C:
		return toStatus(delivery.ErrInvalidStreamRequest)
	case <-ctx.Done():
		return toStatus(ctx.Err())
	}

	sub, err := h.svc.Open(ctx, who.user.ID, chatID, func(c context.Context) error {
		_, err := h.accounts.Authenticate(c, who.token)
		return err
	})
	if err != nil {
		return toStatus(err)
	}
	// Выход из обработчика мимо причин ниже означает внутреннюю ошибку; повторное закрытие ничего не делает.
	defer sub.Close(errStreamInternal)

	writeDone := make(chan error, 1)
	go h.writeStream(stream, sub, writeDone)

	for {
		select {
		case <-sub.Done():
			return toStatus(sub.Err())
		case err := <-writeDone:
			return clientEnded(sub, err)
		case <-ctx.Done():
			sub.Close(ctx.Err())
			return toStatus(ctx.Err())
		case r := <-messages:
			if r.err != nil {
				return clientEnded(sub, r.err)
			}
			ack := r.msg.GetAck()
			if ack == nil {
				sub.Close(delivery.ErrInvalidStreamRequest)
				return toStatus(delivery.ErrInvalidStreamRequest)
			}
			sub.Ack(ack.GetDeliveryId())
		}
	}
}
