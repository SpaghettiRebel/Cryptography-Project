// Пакет memstore — хранилище в памяти для тестов сервисов. В сервер не входит.
package memstore

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"cryptochat/server/internal/store"
)

type session struct {
	userID  string
	expires time.Time
}

type pair struct{ a, b string }

// Store реализует store.AccountStore, store.ContactStore, store.ChatStore и store.DeliveryStore.
type Store struct {
	mu       sync.Mutex
	nextID   int
	users    map[string]store.User // по нормализованному логину
	byID     map[string]store.User
	sessions map[string]session
	requests map[pair]bool // отправитель, получатель
	contacts map[pair]bool // упорядоченная пара
	chats    map[[16]byte]*chatRow
	chatSeq  int
	closeSeq int
}

// New создаёт пустое хранилище.
func New() *Store {
	return &Store{
		users:    map[string]store.User{},
		byID:     map[string]store.User{},
		sessions: map[string]session{},
		requests: map[pair]bool{},
		contacts: map[pair]bool{},
		chats:    map[[16]byte]*chatRow{},
	}
}

// CreateUser создаёт пользователя.
func (m *Store) CreateUser(_ context.Context, login, norm, hash string) (store.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[norm]; ok {
		return store.User{}, store.ErrLoginTaken
	}
	m.nextID++
	u := store.User{ID: fmt.Sprintf("user-%d", m.nextID), Login: login, Hash: hash}
	m.users[norm] = u
	m.byID[u.ID] = u
	return u, nil
}

// UserByLogin ищет пользователя.
func (m *Store) UserByLogin(_ context.Context, norm string) (store.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[norm]
	if !ok {
		return store.User{}, store.ErrNotFound
	}
	return u, nil
}

// CreateSession сохраняет хеш сессии.
func (m *Store) CreateSession(_ context.Context, tokenHash []byte, userID string, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[string(tokenHash)] = session{userID: userID, expires: expires}
	return nil
}

// SessionUser возвращает владельца действующей сессии.
func (m *Store) SessionUser(_ context.Context, tokenHash []byte, now time.Time) (store.UserRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[string(tokenHash)]
	if !ok || !s.expires.After(now) {
		return store.UserRef{}, store.ErrNotFound
	}
	u := m.byID[s.userID]
	return store.UserRef{ID: u.ID, Login: u.Login}, nil
}

// DeleteSession удаляет сессию.
func (m *Store) DeleteSession(_ context.Context, tokenHash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[string(tokenHash)]; !ok {
		return store.ErrNotFound
	}
	delete(m.sessions, string(tokenHash))
	return nil
}

// DeleteExpiredSessions удаляет просроченные сессии пользователя.
func (m *Store) DeleteExpiredSessions(_ context.Context, userID string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, s := range m.sessions {
		if s.userID == userID && !s.expires.After(now) {
			delete(m.sessions, k)
		}
	}
	return nil
}

// HasSession нужен тестам: проверка содержимого хранилища.
func (m *Store) HasSession(tokenHash []byte) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sessions[string(tokenHash)]
	return ok
}

func ordered(a, b string) pair {
	if a < b {
		return pair{a, b}
	}
	return pair{b, a}
}

// SendRequest отправляет запрос или, при встречном запросе, сразу создаёт контакт.
func (m *Store) SendRequest(_ context.Context, senderID, recipientNorm string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.users[recipientNorm]
	if !ok {
		return false, store.ErrNotFound
	}
	if r.ID == senderID {
		return false, store.ErrSelfRequest
	}
	if m.contacts[ordered(senderID, r.ID)] {
		return false, store.ErrAlreadyContact
	}
	if m.requests[pair{senderID, r.ID}] {
		return false, store.ErrDuplicateRequest
	}
	if m.requests[pair{r.ID, senderID}] {
		delete(m.requests, pair{r.ID, senderID})
		m.contacts[ordered(senderID, r.ID)] = true
		return true, nil
	}
	m.requests[pair{senderID, r.ID}] = true
	return false, nil
}

// AcceptRequest принимает входящий запрос.
func (m *Store) AcceptRequest(_ context.Context, recipientID, senderNorm string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.users[senderNorm]
	if !ok || !m.requests[pair{s.ID, recipientID}] {
		return store.ErrRequestNotFound
	}
	delete(m.requests, pair{s.ID, recipientID})
	m.contacts[ordered(s.ID, recipientID)] = true
	return nil
}

// RejectRequest отклоняет входящий запрос.
func (m *Store) RejectRequest(_ context.Context, recipientID, senderNorm string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.users[senderNorm]
	if !ok || !m.requests[pair{s.ID, recipientID}] {
		return store.ErrRequestNotFound
	}
	delete(m.requests, pair{s.ID, recipientID})
	return nil
}

// RemoveContact удаляет контакт у обоих.
func (m *Store) RemoveContact(_ context.Context, userID, otherNorm string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.users[otherNorm]
	if !ok || !m.contacts[ordered(userID, o.ID)] {
		return store.ErrContactNotFound
	}
	delete(m.contacts, ordered(userID, o.ID))
	m.closePairChats(userID, o.ID)
	return nil
}

// ListContacts возвращает списки, отсортированные по логину.
func (m *Store) ListContacts(_ context.Context, userID string) (store.ContactLists, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out store.ContactLists
	ref := func(id string) store.UserRef {
		u := m.byID[id]
		return store.UserRef{ID: u.ID, Login: u.Login}
	}
	for p := range m.contacts {
		switch userID {
		case p.a:
			out.Contacts = append(out.Contacts, ref(p.b))
		case p.b:
			out.Contacts = append(out.Contacts, ref(p.a))
		}
	}
	for p := range m.requests {
		switch userID {
		case p.a:
			out.Outgoing = append(out.Outgoing, ref(p.b))
		case p.b:
			out.Incoming = append(out.Incoming, ref(p.a))
		}
	}
	for _, list := range [][]store.UserRef{out.Contacts, out.Incoming, out.Outgoing} {
		sort.Slice(list, func(i, j int) bool { return list[i].Login < list[j].Login })
	}
	return out, nil
}
