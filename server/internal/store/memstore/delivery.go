package memstore

import (
	"context"
	"sort"

	"cryptochat/server/internal/store"
)

// DeliveryState возвращает состояние чата для проверки фрагмента.
func (m *Store) DeliveryState(_ context.Context, id [16]byte, userID string) (store.DeliveryState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := m.find(id, userID)
	if err != nil {
		return store.DeliveryState{}, err
	}
	self, peer := c.member(userID)
	return store.DeliveryState{
		PeerID:    peer.id,
		Closed:    c.closed != nil,
		Connected: self.connected,
		Ready:     self.value != nil && peer.value != nil,
	}, nil
}

// PendingQueueCleanup возвращает закрытые чаты без отметки, самые давно закрытые первыми.
func (m *Store) PendingQueueCleanup(_ context.Context, limit int) ([]store.ChatMembers, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var rows []*chatRow
	for _, c := range m.chats {
		if c.closed != nil && !c.queuesDeleted {
			rows = append(rows, c)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].closeSeq < rows[j].closeSeq })
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]store.ChatMembers, 0, len(rows))
	for _, c := range rows {
		out = append(out, store.ChatMembers{ID: c.id, UserLow: c.members[0].id, UserHigh: c.members[1].id})
	}
	return out, nil
}

// MarkQueuesDeleted ставит отметку у закрытого чата. Активный и неизвестный чат не меняются.
func (m *Store) MarkQueuesDeleted(_ context.Context, id [16]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.chats[id]; ok && c.closed != nil {
		c.queuesDeleted = true
	}
	return nil
}

// ReopenQueueCleanup снимает отметку у закрытого чата. Активный и неизвестный чат не меняются.
func (m *Store) ReopenQueueCleanup(_ context.Context, id [16]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.chats[id]; ok && c.closed != nil {
		c.queuesDeleted = false
	}
	return nil
}
