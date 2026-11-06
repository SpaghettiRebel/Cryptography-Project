package rpc

import (
	"context"

	apiv1 "cryptochat/server/api/v1"
	"cryptochat/server/internal/accounts"
	"cryptochat/server/internal/contacts"
	"cryptochat/server/internal/store"
)

type accountsHandler struct {
	apiv1.UnimplementedAccountsServer
	svc *accounts.Service
}

func (h *accountsHandler) Register(ctx context.Context, req *apiv1.RegisterRequest) (*apiv1.RegisterResponse, error) {
	u, err := h.svc.Register(ctx, req.GetLogin(), req.GetPassword())
	if err != nil {
		return nil, toStatus(err)
	}
	return &apiv1.RegisterResponse{UserId: u.ID, Login: u.Login}, nil
}

func (h *accountsHandler) Login(ctx context.Context, req *apiv1.LoginRequest) (*apiv1.LoginResponse, error) {
	s, err := h.svc.Login(ctx, req.GetLogin(), req.GetPassword())
	if err != nil {
		return nil, toStatus(err)
	}
	return &apiv1.LoginResponse{Token: s.Token, ExpiresAt: s.ExpiresAt.Unix(), UserId: s.User.ID, Login: s.User.Login}, nil
}

func (h *accountsHandler) Logout(ctx context.Context, _ *apiv1.LogoutRequest) (*apiv1.LogoutResponse, error) {
	if err := h.svc.Logout(ctx, identityFrom(ctx).token); err != nil {
		return nil, toStatus(err)
	}
	return &apiv1.LogoutResponse{}, nil
}

type contactsHandler struct {
	apiv1.UnimplementedContactsServer
	svc *contacts.Service
}

func (h *contactsHandler) SendRequest(ctx context.Context, req *apiv1.SendContactRequest) (*apiv1.SendContactResponse, error) {
	became, err := h.svc.SendRequest(ctx, identityFrom(ctx).user.ID, req.GetLogin())
	if err != nil {
		return nil, toStatus(err)
	}
	return &apiv1.SendContactResponse{BecameContact: became}, nil
}

func (h *contactsHandler) AcceptRequest(ctx context.Context, req *apiv1.ContactLogin) (*apiv1.ContactChanged, error) {
	if err := h.svc.AcceptRequest(ctx, identityFrom(ctx).user.ID, req.GetLogin()); err != nil {
		return nil, toStatus(err)
	}
	return &apiv1.ContactChanged{}, nil
}

func (h *contactsHandler) RejectRequest(ctx context.Context, req *apiv1.ContactLogin) (*apiv1.ContactChanged, error) {
	if err := h.svc.RejectRequest(ctx, identityFrom(ctx).user.ID, req.GetLogin()); err != nil {
		return nil, toStatus(err)
	}
	return &apiv1.ContactChanged{}, nil
}

func (h *contactsHandler) RemoveContact(ctx context.Context, req *apiv1.ContactLogin) (*apiv1.ContactChanged, error) {
	if err := h.svc.RemoveContact(ctx, identityFrom(ctx).user.ID, req.GetLogin()); err != nil {
		return nil, toStatus(err)
	}
	return &apiv1.ContactChanged{}, nil
}

func refs(list []store.UserRef) []*apiv1.UserRef {
	out := make([]*apiv1.UserRef, 0, len(list))
	for _, u := range list {
		out = append(out, &apiv1.UserRef{UserId: u.ID, Login: u.Login})
	}
	return out
}

func (h *contactsHandler) ListContacts(ctx context.Context, _ *apiv1.ListContactsRequest) (*apiv1.ListContactsResponse, error) {
	lists, err := h.svc.List(ctx, identityFrom(ctx).user.ID)
	if err != nil {
		return nil, toStatus(err)
	}
	return &apiv1.ListContactsResponse{Contacts: refs(lists.Contacts), Incoming: refs(lists.Incoming), Outgoing: refs(lists.Outgoing)}, nil
}
