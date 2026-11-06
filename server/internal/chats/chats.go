// Пакет chats реализует жизненный цикл секретного чата и подключение участников. Сервер хранит выбор
// алгоритма как параметр и не шифрует данные.
package chats

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"cryptochat/server/internal/accounts"
	"cryptochat/server/internal/store"
)

// ErrInvalidChatID — идентификатор не из 16 байт.
var ErrInvalidChatID = errors.New("chats: идентификатор чата должен занимать 16 байт")

// ParamError называет неверный параметр чата.
type ParamError struct {
	// Field — algorithm, key_bits, cipher_mode или padding_mode.
	Field string
}

func (e ParamError) Error() string { return "chats: неверный параметр " + e.Field }

// Service — сервис чатов.
type Service struct {
	store  store.ChatStore
	random io.Reader
}

// New создаёт сервис.
func New(s store.ChatStore) *Service { return &Service{store: s, random: rand.Reader} }

// ParseID проверяет длину идентификатора.
func ParseID(raw []byte) ([16]byte, error) {
	var id [16]byte
	if len(raw) != len(id) {
		return id, ErrInvalidChatID
	}
	copy(id[:], raw)
	return id, nil
}

func validate(p store.ChatParams) error {
	switch {
	case p.Algorithm < 1 || p.Algorithm > 2:
		return ParamError{Field: "algorithm"}
	case p.KeyBits != 128 && p.KeyBits != 192 && p.KeyBits != 256:
		return ParamError{Field: "key_bits"}
	case p.CipherMode < 1 || p.CipherMode > 7:
		return ParamError{Field: "cipher_mode"}
	case p.PaddingMode < 1 || p.PaddingMode > 4:
		return ParamError{Field: "padding_mode"}
	}
	return nil
}

// Create создаёт чат с контактом. Несуществующий логин не отличается от логина не контакта.
func (s *Service) Create(ctx context.Context, userID, peerLogin string, p store.ChatParams) (store.Chat, error) {
	if err := validate(p); err != nil {
		return store.Chat{}, err
	}
	if !accounts.ValidLogin(peerLogin) {
		return store.Chat{}, store.ErrNotContact
	}
	var id [16]byte
	if _, err := io.ReadFull(s.random, id[:]); err != nil {
		return store.Chat{}, fmt.Errorf("chats: источник случайности: %w", err)
	}
	return s.store.CreateChat(ctx, id, userID, accounts.NormalizeLogin(peerLogin), p)
}

// List возвращает активные чаты пользователя.
func (s *Service) List(ctx context.Context, userID string) ([]store.Chat, error) {
	return s.store.ListActiveChats(ctx, userID)
}

// Get возвращает любой чат пользователя, в том числе закрытый.
func (s *Service) Get(ctx context.Context, userID string, rawID []byte) (store.Chat, error) {
	id, err := ParseID(rawID)
	if err != nil {
		return store.Chat{}, err
	}
	return s.store.GetChat(ctx, id, userID)
}

// Connect подключает участника.
func (s *Service) Connect(ctx context.Context, userID string, rawID []byte) (store.Chat, error) {
	return s.setConnected(ctx, userID, rawID, true)
}

// Disconnect отключает участника и не закрывает чат.
func (s *Service) Disconnect(ctx context.Context, userID string, rawID []byte) (store.Chat, error) {
	return s.setConnected(ctx, userID, rawID, false)
}

func (s *Service) setConnected(ctx context.Context, userID string, rawID []byte, connected bool) (store.Chat, error) {
	id, err := ParseID(rawID)
	if err != nil {
		return store.Chat{}, err
	}
	return s.store.SetConnected(ctx, id, userID, connected)
}

// Close окончательно закрывает чат.
func (s *Service) Close(ctx context.Context, userID string, rawID []byte) (store.Chat, error) {
	id, err := ParseID(rawID)
	if err != nil {
		return store.Chat{}, err
	}
	return s.store.CloseChat(ctx, id, userID)
}

// Ready сообщает, что чат активен и опубликованы оба значения.
func Ready(c store.Chat) bool {
	return c.ClosedAt == nil && c.Self.HasPublicValue && c.Peer.HasPublicValue
}
