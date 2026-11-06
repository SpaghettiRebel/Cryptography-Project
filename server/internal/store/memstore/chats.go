package memstore

import (
	"bytes"
	"context"
	"sort"
	"time"

	"cryptochat/server/internal/store"
)

type chatMember struct {
	id        string
	connected bool
	value     []byte
}

type chatRow struct {
	id      [16]byte
	seq     int
	creator string
	params  store.ChatParams
	members [2]*chatMember
	created time.Time
	closed  *time.Time
	// closeSeq упорядочивает закрытия, queuesDeleted — отметка об удалении очередей. Закрытие её не меняет.
	closeSeq      int
	queuesDeleted bool
}

func (c *chatRow) member(userID string) (self, peer *chatMember) {
	switch userID {
	case c.members[0].id:
		return c.members[0], c.members[1]
	case c.members[1].id:
		return c.members[1], c.members[0]
	}
	return nil, nil
}

func (c *chatRow) close(now time.Time, seq int) {
	if c.closed != nil {
		return
	}
	c.closed = &now
	c.closeSeq = seq
	for _, m := range c.members {
		m.connected = false
		m.value = nil
	}
}

func (m *Store) ref(id string) store.UserRef {
	u := m.byID[id]
	return store.UserRef{ID: u.ID, Login: u.Login}
}

func (m *Store) participant(p *chatMember) store.ChatParticipant {
	return store.ChatParticipant{User: m.ref(p.id), Connected: p.connected, HasPublicValue: p.value != nil}
}

func (m *Store) view(c *chatRow, userID string) store.Chat {
	self, peer := c.member(userID)
	return store.Chat{
		ID:        c.id,
		Creator:   m.ref(c.creator),
		Params:    c.params,
		Self:      m.participant(self),
		Peer:      m.participant(peer),
		CreatedAt: c.created,
		ClosedAt:  c.closed,
	}
}

// find возвращает чат, в котором участвует пользователь.
func (m *Store) find(id [16]byte, userID string) (*chatRow, error) {
	c, ok := m.chats[id]
	if !ok {
		return nil, store.ErrChatNotFound
	}
	if self, _ := c.member(userID); self == nil {
		return nil, store.ErrChatNotFound
	}
	return c, nil
}

// CreateChat создаёт чат с контактом.
func (m *Store) CreateChat(_ context.Context, id [16]byte, creatorID, peerNorm string, p store.ChatParams) (store.Chat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	peer, ok := m.users[peerNorm]
	if !ok {
		return store.Chat{}, store.ErrNotContact
	}
	if peer.ID == creatorID {
		return store.Chat{}, store.ErrSelfChat
	}
	if !m.contacts[ordered(creatorID, peer.ID)] {
		return store.Chat{}, store.ErrNotContact
	}
	m.chatSeq++
	c := &chatRow{
		id:      id,
		seq:     m.chatSeq,
		creator: creatorID,
		params:  p,
		members: [2]*chatMember{{id: creatorID, connected: true}, {id: peer.ID}},
		created: time.Now(),
	}
	m.chats[id] = c
	return m.view(c, creatorID), nil
}

// ListActiveChats возвращает активные чаты, новые первыми.
func (m *Store) ListActiveChats(_ context.Context, userID string) ([]store.Chat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var rows []*chatRow
	for _, c := range m.chats {
		if self, _ := c.member(userID); self != nil && c.closed == nil {
			rows = append(rows, c)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].seq > rows[j].seq })
	out := make([]store.Chat, 0, len(rows))
	for _, c := range rows {
		out = append(out, m.view(c, userID))
	}
	return out, nil
}

// GetChat возвращает чат участника.
func (m *Store) GetChat(_ context.Context, id [16]byte, userID string) (store.Chat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := m.find(id, userID)
	if err != nil {
		return store.Chat{}, err
	}
	return m.view(c, userID), nil
}

// SetConnected меняет подключение участника.
func (m *Store) SetConnected(_ context.Context, id [16]byte, userID string, connected bool) (store.Chat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := m.find(id, userID)
	if err != nil {
		return store.Chat{}, err
	}
	if c.closed != nil {
		if connected {
			return store.Chat{}, store.ErrChatClosed
		}
		return m.view(c, userID), nil
	}
	self, _ := c.member(userID)
	self.connected = connected
	return m.view(c, userID), nil
}

// CloseChat окончательно закрывает чат.
func (m *Store) CloseChat(_ context.Context, id [16]byte, userID string) (store.Chat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := m.find(id, userID)
	if err != nil {
		return store.Chat{}, err
	}
	m.closeSeq++
	c.close(time.Now(), m.closeSeq)
	return m.view(c, userID), nil
}

// PublishValue сохраняет значение подключённого участника.
func (m *Store) PublishValue(_ context.Context, id [16]byte, userID string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := m.find(id, userID)
	if err != nil {
		return err
	}
	if c.closed != nil {
		return store.ErrChatClosed
	}
	self, _ := c.member(userID)
	switch {
	case !self.connected:
		return store.ErrNotConnected
	case self.value == nil:
		self.value = append([]byte(nil), value...)
	case !bytes.Equal(self.value, value):
		return store.ErrValueConflict
	}
	return nil
}

// PeerValue возвращает копию значения собеседника или nil.
func (m *Store) PeerValue(_ context.Context, id [16]byte, userID string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := m.find(id, userID)
	if err != nil {
		return nil, err
	}
	if c.closed != nil {
		return nil, store.ErrChatClosed
	}
	_, peer := c.member(userID)
	if peer.value == nil {
		return nil, nil
	}
	return append([]byte(nil), peer.value...), nil
}

// closePairChats закрывает активные чаты пары. Вызывается под m.mu.
func (m *Store) closePairChats(a, b string) {
	now := time.Now()
	for _, c := range m.chats {
		if self, _ := c.member(a); self != nil {
			if other, _ := c.member(b); other != nil {
				m.closeSeq++
				c.close(now, m.closeSeq)
			}
		}
	}
}
