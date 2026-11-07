package service

import (
	"slices"
	"strings"
	"time"

	"cryptochat/client/internal/keyagree"
	"cryptochat/core/cipher"
)

// Состояние связи.
const (
	Online     = "online"
	Offline    = "offline"
	Connecting = "connecting"
)

// SessionInfo — вошедший пользователь.
type SessionInfo struct {
	Login  string
	UserID string
}

// ChatInfo — активный чат для интерфейса. Ключей, секретов и открытых значений здесь нет.
type ChatInfo struct {
	ID            [16]byte
	Peer          string
	Creator       string
	Algorithm     cipher.Algorithm
	KeyBits       int
	Mode          int
	Padding       int
	CreatedAt     time.Time
	SelfConnected bool
	PeerConnected bool
	KeyState      keyagree.State
	AlgorithmNote string
}

// Snapshot — неизменяемый снимок состояния. Получатель не должен менять срезы.
type Snapshot struct {
	Version    uint64
	Connection string
	Session    *SessionInfo
	Contacts   []string
	Incoming   []string
	Outgoing   []string
	Chats      []ChatInfo
}

func sortedLogins(in []string) []string {
	out := slices.Clone(in)
	if out == nil {
		out = []string{}
	}
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out
}

// sameContent сравнивает снимки без учёта версии.
func sameContent(a, b Snapshot) bool {
	if a.Connection != b.Connection || (a.Session == nil) != (b.Session == nil) {
		return false
	}
	if a.Session != nil && *a.Session != *b.Session {
		return false
	}
	return slices.Equal(a.Contacts, b.Contacts) && slices.Equal(a.Incoming, b.Incoming) &&
		slices.Equal(a.Outgoing, b.Outgoing) && slices.Equal(a.Chats, b.Chats)
}
