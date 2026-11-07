package service

import (
	"encoding/hex"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/ciphers"
	"cryptochat/client/internal/keyagree"
	"cryptochat/client/internal/localdb"
	"cryptochat/client/internal/rpcclient"
	"cryptochat/core/cipher"
)

// CreateChatRequest — параметры нового чата из интерфейса.
type CreateChatRequest struct {
	Peer       string
	Algorithm  string
	KeyBits    int
	CipherMode string
	Padding    string
}

// ChatOptions — варианты для создания чата по составу сборки.
func (s *Service) ChatOptions() ciphers.ChatOptions { return ciphers.Options() }

func parseChatID(id string) ([16]byte, error) {
	var out [16]byte
	raw, err := hex.DecodeString(id)
	if err != nil || len(raw) != 16 {
		return out, apperr.New(apperr.InvalidChatID)
	}
	copy(out[:], raw)
	return out, nil
}

func parseParams(req CreateChatRequest) (rpcclient.ChatParams, error) {
	alg, ok := ciphers.AlgorithmByID(req.Algorithm)
	if !ok {
		return rpcclient.ChatParams{}, apperr.WithField(apperr.InvalidChatParams, "algorithm")
	}
	if !cipher.ValidKeyBits(req.KeyBits) {
		return rpcclient.ChatParams{}, apperr.WithField(apperr.InvalidChatParams, "key_bits")
	}
	mode, ok := ciphers.ModeNumber(req.CipherMode)
	if !ok {
		return rpcclient.ChatParams{}, apperr.WithField(apperr.InvalidChatParams, "cipher_mode")
	}
	pad, ok := ciphers.PaddingNumber(req.Padding)
	if !ok {
		return rpcclient.ChatParams{}, apperr.WithField(apperr.InvalidChatParams, "padding_mode")
	}
	return rpcclient.ChatParams{Algorithm: alg, KeyBits: req.KeyBits, Mode: mode, Padding: pad}, nil
}

// CreateChat создаёт чат. Алгоритм без реализации в сборке отклоняется до обращения к серверу; на сервер
// всегда уходит выбранный алгоритм, а не тестовая подстановка.
func (s *Service) CreateChat(req CreateChatRequest) (ChatInfo, error) {
	params, err := parseParams(req)
	if err != nil {
		return ChatInfo{}, err
	}
	if !ciphers.Info(params.Algorithm).Available {
		return ChatInfo{}, apperr.New(apperr.AlgorithmUnavailable)
	}
	var id [16]byte
	err = s.action(func(sess *session) error {
		c, err := sess.api.CreateChat(sess.ctx, req.Peer, params)
		if err != nil {
			return err
		}
		id = c.ID
		if err := s.remember(sess, c); err != nil {
			return err
		}
		s.advance(sess, c)
		return nil
	})
	if err != nil {
		return ChatInfo{}, err
	}
	return s.chatByID(id)
}

// ConnectChat подключает к чату и, если ключа нет, сразу продвигает согласование.
func (s *Service) ConnectChat(chatID string) (ChatInfo, error) {
	return s.chatAction(chatID, func(sess *session, id [16]byte) (rpcclient.Chat, error) {
		return sess.api.ConnectChat(sess.ctx, id)
	}, true)
}

// DisconnectChat отключает от чата; ключ остаётся на устройстве.
func (s *Service) DisconnectChat(chatID string) (ChatInfo, error) {
	return s.chatAction(chatID, func(sess *session, id [16]byte) (rpcclient.Chat, error) {
		return sess.api.DisconnectChat(sess.ctx, id)
	}, false)
}

func (s *Service) chatAction(chatID string, call func(*session, [16]byte) (rpcclient.Chat, error), advance bool) (ChatInfo, error) {
	id, err := parseChatID(chatID)
	if err != nil {
		return ChatInfo{}, err
	}
	err = s.action(func(sess *session) error {
		c, err := call(sess, id)
		if apperr.Is(err, apperr.ChatClosed, apperr.ChatNotFound) {
			s.forgetChat(sess, id)
		}
		if err != nil {
			return err
		}
		if c.Closed() {
			s.forgetChat(sess, id)
			return apperr.New(apperr.ChatClosed)
		}
		if err := s.remember(sess, c); err != nil {
			return err
		}
		if advance {
			s.advance(sess, c)
		}
		return nil
	})
	if err != nil {
		return ChatInfo{}, err
	}
	return s.chatByID(id)
}

// CloseChat закрывает чат для обоих и удаляет ключ и секрет с устройства. Повтор безопасен.
func (s *Service) CloseChat(chatID string) error {
	id, err := parseChatID(chatID)
	if err != nil {
		return err
	}
	return s.action(func(sess *session) error {
		_, err := sess.api.CloseChat(sess.ctx, id)
		if err == nil || apperr.Is(err, apperr.ChatNotFound) {
			s.forgetChat(sess, id)
		}
		return err
	})
}

// remember записывает чат в локальную базу и в состояние сессии. Чат, уже закрытый на устройстве, не
// записывается: список с сервера мог быть получен до закрытия.
func (s *Service) remember(sess *session, c rpcclient.Chat) error {
	local := localdb.Chat{
		ID: c.ID, Peer: c.Peer.Login, Creator: c.Creator, Algorithm: c.Params.Algorithm, KeyBits: c.Params.KeyBits,
		Mode: c.Params.Mode, Padding: c.Params.Padding, CreatedAt: c.CreatedAt,
	}
	// keyMu упорядочивает запись с forgetChat: закрытие не проходит между проверкой и записью.
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	if s.closedLocally(sess, c.ID) {
		return nil
	}
	if err := sess.db.UpsertChat(local); err != nil {
		s.log.Printf("запись чата: %v", err)
		return apperr.Profile(sess.db.Dir())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess.ended {
		return nil
	}
	st, ok := sess.chats[c.ID]
	if !ok {
		st = &chatState{key: keyagree.LocalState(sess.db, c.ID)}
		sess.chats[c.ID] = st
	}
	st.local = local
	st.selfConnected = c.Self.Connected
	st.peerConnected = c.Peer.Connected
	return nil
}

func (s *Service) chatByID(id [16]byte) (ChatInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sess == nil {
		return ChatInfo{}, apperr.New(apperr.NotLoggedIn)
	}
	st, ok := s.sess.chats[id]
	if !ok {
		return ChatInfo{}, apperr.New(apperr.ChatNotFound)
	}
	return chatInfo(st), nil
}

// closedLocally сообщает, что чат закрыт на устройстве в этой сессии.
func (s *Service) closedLocally(sess *session, id [16]byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sess.closed[id]
}

// forgetChat отмечает чат закрытым на устройстве, удаляет его секрет и ключ и убирает из списка активных.
func (s *Service) forgetChat(sess *session, id [16]byte) {
	s.keyMu.Lock()
	s.mu.Lock()
	if sess.closed == nil {
		sess.closed = map[[16]byte]bool{}
	}
	sess.closed[id] = true
	s.mu.Unlock()
	if err := sess.db.MarkChatClosed(id); err != nil {
		s.log.Printf("отметка закрытия чата: %v", err)
	}
	if err := sess.db.DeleteChatSecrets(id); err != nil {
		s.log.Printf("удаление ключа чата: %v", err)
	}
	s.keyMu.Unlock()
	s.mu.Lock()
	delete(sess.chats, id)
	s.mu.Unlock()
	s.publish()
}

// advance продвигает согласование ключа чата и запоминает состояние. Закрытый или исчезнувший чат
// убирает следующая сверка списка, она же сообщает об этом пользователю. Чат, закрытый на устройстве,
// пропускается: для него не создаётся новый секрет.
func (s *Service) advance(sess *session, c rpcclient.Chat) {
	s.keyMu.Lock()
	if s.closedLocally(sess, c.ID) {
		s.keyMu.Unlock()
		return
	}
	state, err := keyagree.Advance(sess.ctx, sess.api, sess.db, c)
	s.keyMu.Unlock()
	switch {
	case sess.ctx.Err() != nil:
		return
	case apperr.Is(err, apperr.ChatClosed, apperr.ChatNotFound):
		return
	case apperr.Is(err, apperr.ProfileStorage):
		s.storageFault(sess)
	case err != nil && !apperr.Is(err, apperr.PeerValueInvalid):
		s.observe(sess, err)
	}
	s.mu.Lock()
	st, ok := sess.chats[c.ID]
	// О недопустимом значении собеседника сообщается один раз, при переходе в это состояние.
	invalid := ok && state == keyagree.PeerValueInvalid && st.key != keyagree.PeerValueInvalid
	if ok {
		st.key = state
	}
	s.mu.Unlock()
	if invalid {
		s.notice(NoticeError, apperr.PeerValueInvalid, "Чат с "+c.Peer.Login+": "+apperr.New(apperr.PeerValueInvalid).Message)
	}
}
