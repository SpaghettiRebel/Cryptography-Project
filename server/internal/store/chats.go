package store

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotContact    = errors.New("store: собеседник не найден или не контакт")
	ErrSelfChat      = errors.New("store: чат с самим собой")
	ErrChatNotFound  = errors.New("store: чат не найден")
	ErrChatClosed    = errors.New("store: чат закрыт")
	ErrNotConnected  = errors.New("store: участник не подключён")
	ErrValueConflict = errors.New("store: опубликовано другое значение")
)

// ChatParams — неизменяемые параметры чата. Числа совпадают с перечислениями в chats.proto.
type ChatParams struct {
	Algorithm   int
	KeyBits     int
	CipherMode  int
	PaddingMode int
}

// ChatParticipant — участник без самого открытого значения.
type ChatParticipant struct {
	User           UserRef
	Connected      bool
	HasPublicValue bool
}

// Chat — чат с точки зрения одного из участников.
type Chat struct {
	ID        [16]byte
	Creator   UserRef
	Params    ChatParams
	Self      ChatParticipant
	Peer      ChatParticipant
	CreatedAt time.Time
	// ClosedAt равен nil, пока чат активен.
	ClosedAt *time.Time
}

// ChatStore выполняет каждую операцию атомарно. Для пользователя, не участвующего в чате, и для
// несуществующего чата возвращается одна и та же ошибка ErrChatNotFound.
type ChatStore interface {
	// CreateChat проверяет контакт и создаёт чат: создатель подключён, собеседник нет.
	CreateChat(ctx context.Context, id [16]byte, creatorID, peerNorm string, p ChatParams) (Chat, error)
	// ListActiveChats возвращает активные чаты, новые первыми.
	ListActiveChats(ctx context.Context, userID string) ([]Chat, error)
	GetChat(ctx context.Context, id [16]byte, userID string) (Chat, error)
	// SetConnected идемпотентна. Подключение к закрытому чату даёт ErrChatClosed, а отключение успешно.
	SetConnected(ctx context.Context, id [16]byte, userID string, connected bool) (Chat, error)
	// CloseChat окончательно закрывает чат, отключает обоих и удаляет значения. Повтор успешен.
	CloseChat(ctx context.Context, id [16]byte, userID string) (Chat, error)
	// PublishValue проверяет закрытие, подключение и совпадение с уже сохранённым значением.
	PublishValue(ctx context.Context, id [16]byte, userID string, value []byte) error
	// PeerValue возвращает nil, если собеседник ещё не опубликовал значение.
	PeerValue(ctx context.Context, id [16]byte, userID string) ([]byte, error)
}
