package service

import (
	"strings"
)

// action выполняет действие пользователя в сессии: вызов сервера, учёт связи и внеочередная
// синхронизация после успеха.
func (s *Service) action(f func(sess *session) error) error {
	sess, err := s.current()
	if err != nil {
		return err
	}
	if err := s.observe(sess, f(sess)); err != nil {
		return err
	}
	s.syncNow(sess)
	return nil
}

// SendContactRequest отправляет запрос; встречный запрос сразу создаёт контакт.
func (s *Service) SendContactRequest(login string) (bool, error) {
	var became bool
	err := s.action(func(sess *session) (err error) {
		became, err = sess.api.SendContactRequest(sess.ctx, login)
		return err
	})
	if err == nil && became {
		s.notice(NoticeInfo, "CONTACT_CREATED", "Встречный запрос: "+login+" теперь в контактах")
	}
	return became, err
}

// AcceptContact принимает входящий запрос.
func (s *Service) AcceptContact(login string) error {
	return s.action(func(sess *session) error { return sess.api.AcceptContact(sess.ctx, login) })
}

// RejectContact отклоняет входящий запрос.
func (s *Service) RejectContact(login string) error {
	return s.action(func(sess *session) error { return sess.api.RejectContact(sess.ctx, login) })
}

// RemoveContact удаляет контакт. Сервер закрывает чаты с ним; на устройстве их ключи удаляются сразу,
// без уведомления о закрытии собеседником.
func (s *Service) RemoveContact(login string) error {
	return s.action(func(sess *session) error {
		if err := sess.api.RemoveContact(sess.ctx, login); err != nil {
			return err
		}
		s.mu.Lock()
		var ids [][16]byte
		for id, c := range sess.chats {
			if strings.EqualFold(c.local.Peer, login) {
				ids = append(ids, id)
			}
		}
		s.mu.Unlock()
		for _, id := range ids {
			s.forgetChat(sess, id)
		}
		return nil
	})
}
