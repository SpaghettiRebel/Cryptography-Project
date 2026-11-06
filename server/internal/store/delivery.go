package store

import "context"

// DeliveryState — всё, что нужно для проверки одного фрагмента.
type DeliveryState struct {
	PeerID    string
	Closed    bool
	Connected bool // подключён ли сам пользователь
	Ready     bool // опубликованы оба значения
}

// ChatMembers — закрытый чат, очереди которого ещё не удалены.
type ChatMembers struct {
	ID       [16]byte
	UserLow  string
	UserHigh string
}

// DeliveryStore хранит то, что нужно доставке. Содержимого фрагментов в хранилище нет.
type DeliveryStore interface {
	// DeliveryState — один запрос без пользователей и параметров чата. Для постороннего ErrChatNotFound.
	DeliveryState(ctx context.Context, id [16]byte, userID string) (DeliveryState, error)
	// PendingQueueCleanup возвращает самые давно закрытые первыми, не больше limit.
	PendingQueueCleanup(ctx context.Context, limit int) ([]ChatMembers, error)
	// MarkQueuesDeleted идемпотентна. Активный чат не меняет.
	MarkQueuesDeleted(ctx context.Context, id [16]byte) error
	// ReopenQueueCleanup снимает отметку у закрытого чата: очередь появилась после неё. Активный чат не меняет.
	ReopenQueueCleanup(ctx context.Context, id [16]byte) error
}
