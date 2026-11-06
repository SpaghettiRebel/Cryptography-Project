package contacts_test

import (
	"testing"

	"cryptochat/server/internal/contacts/contactstest"
	"cryptochat/server/internal/store/memstore"
)

func TestTransitions(t *testing.T) { contactstest.RunTransitions(t, memstore.New()) }
