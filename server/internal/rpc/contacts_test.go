package rpc

import (
	"testing"

	"google.golang.org/grpc/codes"

	apiv1 "cryptochat/server/api/v1"
)

func loginsOf(list []*apiv1.UserRef) string {
	s := ""
	for _, u := range list {
		s += u.GetLogin() + ";"
	}
	return s
}

func TestContactsFlow(t *testing.T) {
	h := start(t, nil)
	alice, _ := h.signUp(t, "Alice")
	bob, _ := h.signUp(t, "Bob-1")
	h.signUp(t, "Carol")

	r, err := h.contacts.SendRequest(auth(alice), &apiv1.SendContactRequest{Login: "bob-1"})
	if err != nil || r.GetBecameContact() {
		t.Fatalf("%+v, %v", r, err)
	}
	lists, err := h.contacts.ListContacts(auth(bob), &apiv1.ListContactsRequest{})
	if err != nil || loginsOf(lists.GetIncoming()) != "Alice;" {
		t.Fatalf("%+v, %v", lists, err)
	}
	lists, err = h.contacts.ListContacts(auth(alice), &apiv1.ListContactsRequest{})
	if err != nil || loginsOf(lists.GetOutgoing()) != "Bob-1;" {
		t.Fatalf("%+v, %v", lists, err)
	}

	_, err = h.contacts.SendRequest(auth(alice), &apiv1.SendContactRequest{Login: "Bob-1"})
	expectError(t, err, codes.AlreadyExists, ReasonDuplicateRequest)
	_, err = h.contacts.SendRequest(auth(alice), &apiv1.SendContactRequest{Login: "Alice"})
	expectError(t, err, codes.InvalidArgument, ReasonSelfRequest)
	_, err = h.contacts.SendRequest(auth(alice), &apiv1.SendContactRequest{Login: "nobody"})
	expectError(t, err, codes.NotFound, ReasonUserNotFound)
	_, err = h.contacts.AcceptRequest(auth(alice), &apiv1.ContactLogin{Login: "Bob-1"})
	expectError(t, err, codes.NotFound, ReasonRequestNotFound)
	_, err = h.contacts.RejectRequest(auth(alice), &apiv1.ContactLogin{Login: "Carol"})
	expectError(t, err, codes.NotFound, ReasonRequestNotFound)

	if _, err := h.contacts.AcceptRequest(auth(bob), &apiv1.ContactLogin{Login: "Alice"}); err != nil {
		t.Fatal(err)
	}
	lists, err = h.contacts.ListContacts(auth(alice), &apiv1.ListContactsRequest{})
	if err != nil || loginsOf(lists.GetContacts()) != "Bob-1;" || len(lists.GetOutgoing()) != 0 {
		t.Fatalf("%+v, %v", lists, err)
	}
	_, err = h.contacts.SendRequest(auth(alice), &apiv1.SendContactRequest{Login: "Bob-1"})
	expectError(t, err, codes.AlreadyExists, ReasonAlreadyContact)

	if _, err := h.contacts.RemoveContact(auth(bob), &apiv1.ContactLogin{Login: "Alice"}); err != nil {
		t.Fatal(err)
	}
	_, err = h.contacts.RemoveContact(auth(alice), &apiv1.ContactLogin{Login: "Bob-1"})
	expectError(t, err, codes.NotFound, ReasonContactNotFound)

	// Встречный запрос сразу создаёт контакт.
	if _, err := h.contacts.SendRequest(auth(alice), &apiv1.SendContactRequest{Login: "Bob-1"}); err != nil {
		t.Fatal(err)
	}
	r, err = h.contacts.SendRequest(auth(bob), &apiv1.SendContactRequest{Login: "Alice"})
	if err != nil || !r.GetBecameContact() {
		t.Fatalf("встречный запрос: %+v, %v", r, err)
	}
}

func TestContactsRequireSession(t *testing.T) {
	h := start(t, nil)
	calls := map[string]error{}
	_, calls["SendRequest"] = h.contacts.SendRequest(bg, &apiv1.SendContactRequest{Login: "alice"})
	_, calls["AcceptRequest"] = h.contacts.AcceptRequest(bg, &apiv1.ContactLogin{Login: "alice"})
	_, calls["RejectRequest"] = h.contacts.RejectRequest(bg, &apiv1.ContactLogin{Login: "alice"})
	_, calls["RemoveContact"] = h.contacts.RemoveContact(bg, &apiv1.ContactLogin{Login: "alice"})
	_, calls["ListContacts"] = h.contacts.ListContacts(bg, &apiv1.ListContactsRequest{})
	_, calls["Logout"] = h.accounts.Logout(bg, &apiv1.LogoutRequest{})
	for name, err := range calls {
		t.Run(name, func(t *testing.T) { expectError(t, err, codes.Unauthenticated, ReasonUnauthenticated) })
	}
}
