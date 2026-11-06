package rpc

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "cryptochat/server/api/v1"
	"cryptochat/server/internal/chats"
	"cryptochat/server/internal/keyexchange"
	"cryptochat/server/internal/store"
)

func participant(p store.ChatParticipant) *apiv1.ChatParticipant {
	return &apiv1.ChatParticipant{Login: p.User.Login, Connected: p.Connected, PublicValuePublished: p.HasPublicValue}
}

func chatMessage(c store.Chat) *apiv1.Chat {
	out := &apiv1.Chat{
		ChatId: append([]byte(nil), c.ID[:]...),
		Params: &apiv1.ChatParams{
			Algorithm:   apiv1.Algorithm(c.Params.Algorithm),
			KeyBits:     uint32(c.Params.KeyBits),
			CipherMode:  apiv1.CipherMode(c.Params.CipherMode),
			PaddingMode: apiv1.PaddingMode(c.Params.PaddingMode),
		},
		Creator:   c.Creator.Login,
		Self:      participant(c.Self),
		Peer:      participant(c.Peer),
		Ready:     chats.Ready(c),
		CreatedAt: timestamppb.New(c.CreatedAt),
	}
	if c.ClosedAt != nil {
		out.ClosedAt = timestamppb.New(*c.ClosedAt)
	}
	return out
}

func chatReply(c store.Chat, err error) (*apiv1.ChatResponse, error) {
	if err != nil {
		return nil, toStatus(err)
	}
	return &apiv1.ChatResponse{Chat: chatMessage(c)}, nil
}

type chatsHandler struct {
	apiv1.UnimplementedChatsServer
	svc *chats.Service
}

func (h *chatsHandler) CreateChat(ctx context.Context, req *apiv1.CreateChatRequest) (*apiv1.ChatResponse, error) {
	p := req.GetParams()
	return chatReply(h.svc.Create(ctx, identityFrom(ctx).user.ID, req.GetPeerLogin(), store.ChatParams{
		Algorithm:   int(p.GetAlgorithm()),
		KeyBits:     int(p.GetKeyBits()),
		CipherMode:  int(p.GetCipherMode()),
		PaddingMode: int(p.GetPaddingMode()),
	}))
}

func (h *chatsHandler) ListChats(ctx context.Context, _ *apiv1.ListChatsRequest) (*apiv1.ListChatsResponse, error) {
	list, err := h.svc.List(ctx, identityFrom(ctx).user.ID)
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*apiv1.Chat, 0, len(list))
	for _, c := range list {
		out = append(out, chatMessage(c))
	}
	return &apiv1.ListChatsResponse{Chats: out}, nil
}

func (h *chatsHandler) GetChat(ctx context.Context, req *apiv1.ChatRef) (*apiv1.ChatResponse, error) {
	return chatReply(h.svc.Get(ctx, identityFrom(ctx).user.ID, req.GetChatId()))
}

func (h *chatsHandler) ConnectChat(ctx context.Context, req *apiv1.ChatRef) (*apiv1.ChatResponse, error) {
	return chatReply(h.svc.Connect(ctx, identityFrom(ctx).user.ID, req.GetChatId()))
}

func (h *chatsHandler) DisconnectChat(ctx context.Context, req *apiv1.ChatRef) (*apiv1.ChatResponse, error) {
	return chatReply(h.svc.Disconnect(ctx, identityFrom(ctx).user.ID, req.GetChatId()))
}

func (h *chatsHandler) CloseChat(ctx context.Context, req *apiv1.ChatRef) (*apiv1.ChatResponse, error) {
	return chatReply(h.svc.Close(ctx, identityFrom(ctx).user.ID, req.GetChatId()))
}

type keyExchangeHandler struct {
	apiv1.UnimplementedKeyExchangeServer
	svc *keyexchange.Service
}

func (h *keyExchangeHandler) PublishPublicValue(ctx context.Context, req *apiv1.PublishPublicValueRequest) (*apiv1.PublishPublicValueResponse, error) {
	if err := h.svc.Publish(ctx, identityFrom(ctx).user.ID, req.GetChatId(), req.GetPublicValue()); err != nil {
		return nil, toStatus(err)
	}
	return &apiv1.PublishPublicValueResponse{}, nil
}

func (h *keyExchangeHandler) GetPeerPublicValue(ctx context.Context, req *apiv1.ChatRef) (*apiv1.GetPeerPublicValueResponse, error) {
	value, err := h.svc.Peer(ctx, identityFrom(ctx).user.ID, req.GetChatId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &apiv1.GetPeerPublicValueResponse{Published: value != nil, PublicValue: value}, nil
}
