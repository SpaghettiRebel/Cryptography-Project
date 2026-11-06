// Пакет keyexchange хранит и передаёт открытые значения протокола Диффи-Хеллмана. Сервер не вычисляет
// ключ, а проверку значения по параметрам группы выполняет клиент.
package keyexchange

import (
	"context"
	"errors"

	"cryptochat/server/internal/chats"
	"cryptochat/server/internal/store"
)

// PublicValueSize — длина открытого значения: 2048 бит группы 14 из RFC 3526.
const PublicValueSize = 256

// ErrInvalidPublicValue — значение не из 256 байт.
var ErrInvalidPublicValue = errors.New("keyexchange: открытое значение должно занимать 256 байт")

// Service — сервис обмена значениями.
type Service struct {
	store store.ChatStore
}

// New создаёт сервис.
func New(s store.ChatStore) *Service { return &Service{store: s} }

// Publish сохраняет значение участника. Длина проверяется первой: результат не зависит от чата и не
// раскрывает его существование.
func (s *Service) Publish(ctx context.Context, userID string, rawID, value []byte) error {
	if len(value) != PublicValueSize {
		return ErrInvalidPublicValue
	}
	id, err := chats.ParseID(rawID)
	if err != nil {
		return err
	}
	return s.store.PublishValue(ctx, id, userID, append([]byte(nil), value...))
}

// Peer возвращает значение собеседника или nil, если оно ещё не опубликовано.
func (s *Service) Peer(ctx context.Context, userID string, rawID []byte) ([]byte, error) {
	id, err := chats.ParseID(rawID)
	if err != nil {
		return nil, err
	}
	return s.store.PeerValue(ctx, id, userID)
}
