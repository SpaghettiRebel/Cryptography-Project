// Пакет store описывает хранилище сервера независимо от конкретной базы.
package store

import (
	"context"
	"errors"
	"time"
)

var (
	ErrLoginTaken       = errors.New("store: логин занят")
	ErrNotFound         = errors.New("store: не найдено")
	ErrSelfRequest      = errors.New("store: запрос самому себе")
	ErrAlreadyContact   = errors.New("store: уже контакт")
	ErrDuplicateRequest = errors.New("store: запрос уже отправлен")
	ErrRequestNotFound  = errors.New("store: запрос не найден")
	ErrContactNotFound  = errors.New("store: контакт не найден")
	ErrUnavailable      = errors.New("store: хранилище недоступно")
)

// User — учётная запись. Hash содержит только PHC-строку argon2id.
type User struct {
	ID    string
	Login string
	Hash  string
}

// UserRef — пользователь без сведений о пароле.
type UserRef struct {
	ID    string
	Login string
}

// ContactLists — контакты, входящие и исходящие запросы пользователя.
type ContactLists struct {
	Contacts []UserRef
	Incoming []UserRef
	Outgoing []UserRef
}

// AccountStore хранит пользователей и сессии. Сессия хранится только как SHA-256.
type AccountStore interface {
	CreateUser(ctx context.Context, login, loginNorm, hash string) (User, error)
	UserByLogin(ctx context.Context, loginNorm string) (User, error)
	CreateSession(ctx context.Context, tokenHash []byte, userID string, expiresAt time.Time) error
	// SessionUser возвращает ErrNotFound для отсутствующей и просроченной сессии.
	SessionUser(ctx context.Context, tokenHash []byte, now time.Time) (UserRef, error)
	DeleteSession(ctx context.Context, tokenHash []byte) error
	DeleteExpiredSessions(ctx context.Context, userID string, now time.Time) error
}

// ContactStore выполняет каждую операцию над парой пользователей атомарно: правила переходов из
// data-model.md должны выполняться и при одновременных запросах.
type ContactStore interface {
	// SendRequest возвращает true, если встречный запрос сразу создал контакт.
	SendRequest(ctx context.Context, senderID, recipientNorm string) (bool, error)
	AcceptRequest(ctx context.Context, recipientID, senderNorm string) error
	RejectRequest(ctx context.Context, recipientID, senderNorm string) error
	RemoveContact(ctx context.Context, userID, otherNorm string) error
	ListContacts(ctx context.Context, userID string) (ContactLists, error)
}
