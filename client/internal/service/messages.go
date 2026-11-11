package service

import (
	"net/http"
	"os"
	"strings"
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/ciphers"
	"cryptochat/client/internal/delivery"
	"cryptochat/client/internal/keyagree"
	"cryptochat/client/internal/localdb"
)

// События переписки.
const (
	EventMessages = "messages"
	EventProgress = "progress"
)

// MessageView — сообщение для интерфейса (без шифротекста, IV, ключей и message_id).
type MessageView = delivery.MessageView

// ProgressEvent — событие прогресса стадии сообщения.
type ProgressEvent = delivery.ProgressEvent

// MessagesEvent — изменившиеся сообщения чата; пустой Changed — интерфейс перечитывает ленту.
type MessagesEvent struct {
	ChatID  [16]byte
	Changed []MessageView
}

// MessagePage — страница истории по возрастанию номера.
type MessagePage struct {
	Messages []MessageView
	HasMore  bool
}

// ClosedChatInfo — закрытый чат с числом сообщений истории.
type ClosedChatInfo struct {
	ID           [16]byte
	Peer         string
	Algorithm    string
	KeyBits      int
	Mode         int
	Padding      int
	CreatedAt    time.Time
	ClosedAt     time.Time
	MessageCount int
}

// Пределы истории за один запрос.
const (
	minPage = 1
	maxPage = 200
)

// engineEvents передаёт события доставки сессии в сервис.
type engineEvents struct {
	s    *Service
	sess *session
}

func (ev engineEvents) Messages(chat [16]byte, changed []MessageView) {
	ev.s.emit(EventMessages, MessagesEvent{ChatID: chat, Changed: changed})
	for _, v := range changed {
		if v.Direction == "in" && v.State == "DONE" {
			ev.s.publish()
			return
		}
	}
}

func (ev engineEvents) Progress(p ProgressEvent) { ev.s.emit(EventProgress, p) }

func (ev engineEvents) Notice(kind, code, message string) { ev.s.notice(kind, code, message) }

func (ev engineEvents) ReceiveChanged([16]byte) { ev.s.publish() }

// ChatClosed: сервер сообщил о закрытии или исчезновении чата — внеочередная сверка чатов уберёт его с
// устройства и сообщит пользователю.
func (ev engineEvents) ChatClosed([16]byte) {
	select {
	case ev.sess.kick <- struct{}{}:
	default:
	}
}

func (ev engineEvents) SessionExpired() {
	ev.s.observe(ev.sess, apperr.New(apperr.SessionExpired))
}

// startEngine создаёт доставку сессии и продолжает незавершённое.
func (s *Service) startEngine(sess *session) {
	sess.engine = delivery.New(delivery.Config{
		API:    sess.api,
		Store:  sess.db,
		Chat:   func(id [16]byte) (delivery.ChatInfo, bool) { return s.deliveryChat(sess, id) },
		Events: engineEvents{s: s, sess: sess},
		Logger: s.log,
	})
	sess.engine.Start()
}

// deliveryChat — активный чат для доставки; закрытый на устройстве чат не активен.
func (s *Service) deliveryChat(sess *session, id [16]byte) (delivery.ChatInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := sess.chats[id]
	if !ok || sess.closed[id] {
		return delivery.ChatInfo{}, false
	}
	l := st.local
	return delivery.ChatInfo{ID: id, Algorithm: l.Algorithm, KeyBits: l.KeyBits, Mode: l.Mode, Padding: l.Padding,
		KeyState: st.key}, true
}

// sendBlockLocked — причина недоступности отправки активного чата; пусто — отправка доступна.
// Вызывается под s.mu.
func sendBlockLocked(sess *session, st *chatState) (block, recv, reason string) {
	recv, reason = delivery.RecvOff, ""
	if sess.engine != nil {
		recv, reason = sess.engine.ReceiveState(st.local.ID)
	}
	switch {
	case !ciphers.Info(st.local.Algorithm).Available:
		block = apperr.BlockAlgorithmUnavailable
	case st.key != keyagree.Ready:
		block = apperr.BlockKeyNotReady
	case recv == delivery.RecvElsewhere:
		block = apperr.BlockReceivingElsewhere
	case recv == delivery.RecvOff:
		block = apperr.BlockNotConnected
	}
	return block, recv, reason
}

// session и проверка чата для операций переписки.
func (s *Service) messagingChat(chatID string) (*session, [16]byte, error) {
	id, err := parseChatID(chatID)
	if err != nil {
		return nil, id, err
	}
	sess, err := s.current()
	if err != nil {
		return nil, id, err
	}
	return sess, id, nil
}

// checkSend проверяет доступность отправки в чат.
func (s *Service) checkSend(sess *session, id [16]byte) error {
	s.mu.Lock()
	st, ok := sess.chats[id]
	closed := sess.closed[id]
	var block string
	if ok && !closed {
		block, _, _ = sendBlockLocked(sess, st)
	}
	s.mu.Unlock()
	if !ok || closed {
		if _, known, _ := sess.db.Chat(id); known {
			return apperr.Blocked(apperr.BlockChatClosed)
		}
		return apperr.New(apperr.ChatNotFound)
	}
	if block != "" {
		return apperr.Blocked(block)
	}
	return nil
}

// SendText шифрует и отправляет текст. Пустой после обрезки пробелов текст и текст длиннее 64 КиБ не
// отправляются; сам текст уходит без обрезки.
func (s *Service) SendText(chatID, text string) (MessageView, error) {
	sess, id, err := s.messagingChat(chatID)
	if err != nil {
		return MessageView{}, err
	}
	if strings.TrimSpace(text) == "" {
		return MessageView{}, apperr.WithField(apperr.EmptyText, "text")
	}
	if len(text) > delivery.MaxText {
		return MessageView{}, apperr.WithField(apperr.TextTooLong, "text")
	}
	if err := s.checkSend(sess, id); err != nil {
		return MessageView{}, err
	}
	return sess.engine.SendText(id, text)
}

// SendFile проверяет файл и ставит его в очередь отправки.
func (s *Service) SendFile(chatID, path string) (MessageView, error) {
	sess, id, err := s.messagingChat(chatID)
	if err != nil {
		return MessageView{}, err
	}
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return MessageView{}, apperr.New(apperr.FileRead)
	}
	if st.Size() > delivery.MaxFile {
		return MessageView{}, apperr.New(apperr.FileTooLarge)
	}
	f, err := os.Open(path)
	if err != nil {
		return MessageView{}, apperr.New(apperr.FileRead)
	}
	_ = f.Close()
	if err := s.checkSend(sess, id); err != nil {
		return MessageView{}, err
	}
	return sess.engine.SendFile(id, path, st.Size(), st.ModTime())
}

// ListMessages возвращает страницу истории чата (в том числе закрытого).
func (s *Service) ListMessages(chatID string, beforeSeq int64, limit int) (MessagePage, error) {
	sess, id, err := s.messagingChat(chatID)
	if err != nil {
		return MessagePage{}, err
	}
	if _, ok, err := sess.db.Chat(id); err != nil || !ok {
		return MessagePage{}, apperr.New(apperr.ChatNotFound)
	}
	limit = min(max(limit, minPage), maxPage)
	list, more, err := sess.db.ListMessages(id, beforeSeq, limit)
	if err != nil {
		return MessagePage{}, apperr.New(apperr.LocalWrite)
	}
	page := MessagePage{Messages: make([]MessageView, 0, len(list)), HasMore: more}
	for _, m := range list {
		page.Messages = append(page.Messages, sess.engine.View(m))
	}
	return page, nil
}

// MarkRead отмечает готовые входящие сообщения чата просмотренными.
func (s *Service) MarkRead(chatID string) error {
	sess, id, err := s.messagingChat(chatID)
	if err != nil {
		return err
	}
	n, err := sess.db.MarkRead(id)
	if err != nil {
		return apperr.New(apperr.LocalWrite)
	}
	if n > 0 {
		s.publish()
	}
	return nil
}

// CancelMessage отменяет операцию сообщения.
func (s *Service) CancelMessage(seq int64) (MessageView, error) {
	sess, err := s.current()
	if err != nil {
		return MessageView{}, err
	}
	return sess.engine.CancelMessage(seq)
}

// RetryDecrypt запускает расшифрование снова.
func (s *Service) RetryDecrypt(seq int64) (MessageView, error) {
	sess, err := s.current()
	if err != nil {
		return MessageView{}, err
	}
	return sess.engine.RetryDecrypt(seq)
}

// SaveName — очищенное имя файла сообщения для диалога сохранения; ошибка — сообщение нельзя сохранить.
func (s *Service) SaveName(seq int64) (string, error) {
	sess, err := s.current()
	if err != nil {
		return "", err
	}
	m, ok, err := sess.db.Message(seq)
	if err != nil {
		return "", apperr.New(apperr.LocalWrite)
	}
	if !ok || m.Kind == localdb.KindControl {
		return "", apperr.New(apperr.MessageNotFound)
	}
	if m.Kind != localdb.KindFile && m.Kind != localdb.KindImage {
		return "", apperr.New(apperr.NotAFile)
	}
	return delivery.SafeFileName(m.FileName), nil
}

// SaveFile записывает файл сообщения по выбранному пути.
func (s *Service) SaveFile(seq int64, path string) error {
	sess, err := s.current()
	if err != nil {
		return err
	}
	return sess.engine.SaveFile(seq, path)
}

// ReceiveHere открывает приём чата на этом устройстве (после STREAM_REPLACED).
func (s *Service) ReceiveHere(chatID string) (ChatInfo, error) {
	sess, id, err := s.messagingChat(chatID)
	if err != nil {
		return ChatInfo{}, err
	}
	s.mu.Lock()
	_, ok := sess.chats[id]
	closed := sess.closed[id]
	s.mu.Unlock()
	if !ok || closed {
		if c, known, _ := sess.db.Chat(id); known && !c.ClosedAt.IsZero() || closed {
			return ChatInfo{}, apperr.New(apperr.ChatClosed)
		}
		return ChatInfo{}, apperr.New(apperr.ChatNotFound)
	}
	if err := sess.engine.OpenReceiver(id); err != nil {
		return ChatInfo{}, apperr.New(apperr.LocalWrite)
	}
	s.publish()
	return s.chatByID(id)
}

// ListClosedChats возвращает закрытые чаты профиля с историей.
func (s *Service) ListClosedChats() ([]ClosedChatInfo, error) {
	sess, err := s.current()
	if err != nil {
		return nil, err
	}
	list, err := sess.db.ClosedChats()
	if err != nil {
		return nil, apperr.New(apperr.LocalWrite)
	}
	out := make([]ClosedChatInfo, 0, len(list))
	for _, c := range list {
		out = append(out, ClosedChatInfo{ID: c.ID, Peer: c.Peer, Algorithm: c.Algorithm.String(), KeyBits: c.KeyBits,
			Mode: c.Mode, Padding: c.Padding, CreatedAt: c.CreatedAt, ClosedAt: c.ClosedAt, MessageCount: c.MessageCount})
	}
	return out, nil
}

// DeleteChatHistory удаляет историю закрытого чата с устройства.
func (s *Service) DeleteChatHistory(chatID string) error {
	sess, id, err := s.messagingChat(chatID)
	if err != nil {
		return err
	}
	c, ok, err := sess.db.Chat(id)
	if err != nil {
		return apperr.New(apperr.LocalWrite)
	}
	if !ok {
		return apperr.New(apperr.ChatNotFound)
	}
	if c.ClosedAt.IsZero() {
		return apperr.New(apperr.ChatActive)
	}
	if err := sess.db.DeleteChatHistory(id); err != nil {
		return apperr.New(apperr.LocalWrite)
	}
	s.emit(EventMessages, MessagesEvent{ChatID: id, Changed: []MessageView{}})
	return nil
}

// Media — обработчик картинок /media/<seq> для окна; без сессии — 404.
func (s *Service) Media() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.current()
		if err != nil || sess.engine == nil {
			delivery.NotFound(w)
			return
		}
		sess.engine.ServeMedia(w, r)
	})
}

// unreadCounts читает числа новых сообщений вне блокировки сервиса.
func (s *Service) unreadCounts() map[[16]byte]int {
	s.mu.Lock()
	sess := s.sess
	s.mu.Unlock()
	if sess == nil {
		return nil
	}
	m, err := sess.db.UnreadCounts()
	if err != nil {
		return nil
	}
	return m
}
