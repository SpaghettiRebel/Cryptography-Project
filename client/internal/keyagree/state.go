package keyagree

// State — состояние ключа чата; в интерфейс уходит только этот код.
type State string

const (
	None             State = "NONE"
	WaitingSelf      State = "WAITING_SELF"
	WaitingPeer      State = "WAITING_PEER"
	Ready            State = "READY"
	KeyLost          State = "KEY_LOST"
	PeerValueInvalid State = "PEER_VALUE_INVALID"
)
