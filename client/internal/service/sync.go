package service

import (
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/rpcclient"
)

// loop синхронизирует состояние с сервером раз в интервал и по запросу, пока сессия активна.
// В состоянии offline цикл продолжает попытки с тем же интервалом.
func (s *Service) loop(sess *session) {
	defer s.wg.Done()
	defer close(sess.done)
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-sess.ctx.Done():
			return
		case <-t.C:
		case <-sess.kick:
		}
		s.syncNow(sess)
	}
}

// syncNow выполняет одну синхронизацию; паника на любом шаге даёт уведомление, следующий цикл идёт.
func (s *Service) syncNow(sess *session) {
	s.protect("синхронизация", func() { s.syncOnce(sess) })
}

func (s *Service) syncOnce(sess *session) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	if sess.ctx.Err() != nil {
		return
	}
	faults := s.storageFaultCount(sess)
	contacts, err := sess.api.ListContacts(sess.ctx)
	if s.observe(sess, err) != nil {
		return
	}
	s.mu.Lock()
	sess.contacts = contacts
	s.mu.Unlock()

	chats, err := sess.api.ListChats(sess.ctx)
	if s.observe(sess, err) != nil {
		s.publish()
		return
	}
	active := map[[16]byte]rpcclient.Chat{}
	for _, c := range chats {
		active[c.ID] = c
		if err := s.remember(sess, c); err != nil {
			s.storageFault(sess)
			return
		}
	}
	if !s.reconcile(sess, active) {
		s.publish()
		return
	}
	for _, c := range active {
		if sess.ctx.Err() != nil {
			return
		}
		s.advance(sess, c)
	}
	s.storageRecovered(sess, faults)
	s.publish()
}

// reconcile проверяет чаты, которых нет в списке активных: закрытый или неизвестный серверу чат
// убирается с устройства вместе с ключом. Возвращает false, если проверку нужно повторить позже.
func (s *Service) reconcile(sess *session, active map[[16]byte]rpcclient.Chat) bool {
	known, err := sess.db.KnownActiveChats()
	if err != nil {
		s.log.Printf("чтение чатов профиля: %v", err)
		return false
	}
	s.mu.Lock()
	inMemory := make([][16]byte, 0, len(sess.chats))
	for id := range sess.chats {
		inMemory = append(inMemory, id)
	}
	s.mu.Unlock()
	peers := map[[16]byte]string{}
	for _, c := range known {
		peers[c.ID] = c.Peer
	}
	for _, id := range inMemory {
		if _, ok := peers[id]; !ok {
			if _, ok := active[id]; !ok {
				peers[id] = ""
			}
		}
	}
	for id, peer := range peers {
		if _, ok := active[id]; ok {
			continue
		}
		c, err := sess.api.GetChat(sess.ctx, id)
		switch {
		case err == nil && !c.Closed():
			// Чат активен, но не попал в список (гонка с созданием): учесть при следующей сверке.
			if err := s.remember(sess, c); err != nil {
				s.storageFault(sess)
				return false
			}
		case err == nil:
			s.forgetChat(sess, id)
			s.notice(NoticeInfo, "CHAT_CLOSED_BY_PEER", "Чат с "+peer+" закрыт собеседником, ключ удалён с устройства")
		case apperr.Is(err, apperr.ChatNotFound):
			s.forgetChat(sess, id)
			s.notice(NoticeInfo, apperr.ChatNotFound, "Чат с "+peer+" больше не существует на сервере, ключ удалён с устройства")
		default:
			s.observe(sess, err)
			return false
		}
	}
	return true
}
