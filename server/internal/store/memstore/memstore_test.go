package memstore_test

import (
	"testing"

	"cryptochat/server/internal/chats/chatstest"
	"cryptochat/server/internal/store/memstore"
)

func TestChatRules(t *testing.T) { chatstest.Run(t, memstore.New()) }
