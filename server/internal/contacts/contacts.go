// Пакет contacts реализует запросы на добавление и управление контактами.
package contacts

import (
	"context"

	"cryptochat/server/internal/accounts"
	"cryptochat/server/internal/store"
)

// Service — сервис контактов. Правила переходов выполняет хранилище атомарно.
type Service struct {
	store store.ContactStore
}

// New создаёт сервис.
func New(s store.ContactStore) *Service { return &Service{store: s} }

// SendRequest отправляет запрос; true означает, что встречный запрос сразу создал контакт.
func (s *Service) SendRequest(ctx context.Context, userID, login string) (bool, error) {
	if !accounts.ValidLogin(login) {
		return false, store.ErrNotFound
	}
	return s.store.SendRequest(ctx, userID, accounts.NormalizeLogin(login))
}

// AcceptRequest принимает входящий запрос от login.
func (s *Service) AcceptRequest(ctx context.Context, userID, login string) error {
	if !accounts.ValidLogin(login) {
		return store.ErrRequestNotFound
	}
	return s.store.AcceptRequest(ctx, userID, accounts.NormalizeLogin(login))
}

// RejectRequest отклоняет входящий запрос от login.
func (s *Service) RejectRequest(ctx context.Context, userID, login string) error {
	if !accounts.ValidLogin(login) {
		return store.ErrRequestNotFound
	}
	return s.store.RejectRequest(ctx, userID, accounts.NormalizeLogin(login))
}

// RemoveContact удаляет контакт у обоих.
func (s *Service) RemoveContact(ctx context.Context, userID, login string) error {
	if !accounts.ValidLogin(login) {
		return store.ErrContactNotFound
	}
	return s.store.RemoveContact(ctx, userID, accounts.NormalizeLogin(login))
}

// List возвращает контакты, входящие и исходящие запросы.
func (s *Service) List(ctx context.Context, userID string) (store.ContactLists, error) {
	return s.store.ListContacts(ctx, userID)
}
