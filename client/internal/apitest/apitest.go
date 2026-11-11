// Пакет apitest — сервер в памяти с поведением настоящего API для тестов клиента: учётные записи,
// контакты, чаты и обмен открытыми значениями. Каждый Client — отдельное соединение со своей сессией.
package apitest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/rpcclient"
)

type chat struct {
	id      [16]byte
	params  rpcclient.ChatParams
	creator string
	members [2]string
	conn    map[string]bool
	values  map[string][]byte
	created time.Time
	closed  time.Time
}

// Server — общее состояние для всех клиентов.
type Server struct {
	mu        sync.Mutex
	users     map[string]string // логин в нижнем регистре → пароль
	names     map[string]string // логин в нижнем регистре → логин при регистрации
	tokens    map[string]string // токен → логин в нижнем регистре
	contacts  map[[2]string]bool
	requests  map[[2]string]bool // [от, кому]
	chats     map[[16]byte]*chat
	down      bool
	clock     time.Time
	published int
	d         *deliveryState
}

// NewServer создаёт пустой сервер.
func NewServer() *Server {
	return &Server{users: map[string]string{}, names: map[string]string{}, tokens: map[string]string{},
		contacts: map[[2]string]bool{}, requests: map[[2]string]bool{}, chats: map[[16]byte]*chat{},
		clock: time.UnixMilli(1_700_000_000_000)}
}

// SetDown имитирует недоступный сервер.
func (s *Server) SetDown(down bool) {
	s.mu.Lock()
	s.down = down
	if down {
		s.endAllLocked(apperr.New(apperr.ServerUnavailable))
	}
	s.mu.Unlock()
}

// RevokeAll отзывает все сессии.
func (s *Server) RevokeAll() {
	s.mu.Lock()
	s.tokens = map[string]string{}
	s.mu.Unlock()
}

// SetValue подменяет открытое значение участника (для проверки недопустимых значений).
func (s *Server) SetValue(id [16]byte, login string, v []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.chats[id]; c != nil {
		c.values[strings.ToLower(login)] = slices.Clone(v)
	}
}

// Value возвращает опубликованное значение участника.
func (s *Server) Value(id [16]byte, login string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.chats[id]; c != nil {
		return slices.Clone(c.values[strings.ToLower(login)])
	}
	return nil
}

// Forget удаляет чат, как будто сервер очищен.
func (s *Server) Forget(id [16]byte) {
	s.mu.Lock()
	delete(s.chats, id)
	s.mu.Unlock()
}

// CloseAs закрывает чат от имени участника.
func (s *Server) CloseAs(id [16]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.chats[id]; c != nil && c.closed.IsZero() {
		c.closed = s.tick()
		c.values = map[string][]byte{}
		for k := range c.conn {
			c.conn[k] = false
		}
		s.dropChatLocked(id)
	}
}

// Publications — число успешных публикаций новых значений.
func (s *Server) Publications() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.published
}

func (s *Server) tick() time.Time {
	s.clock = s.clock.Add(time.Second)
	return s.clock
}

func pair(a, b string) [2]string {
	if a > b {
		a, b = b, a
	}
	return [2]string{a, b}
}

// Client — соединение одного клиента. Реализует rpcclient.API.
type Client struct {
	srv *Server

	mu    sync.Mutex
	token string
	calls []string
	hook  func(method string) error

	sendHook func(n int, wire []byte) (bool, error)
	sends    int
}

// SetHook задаёт функцию, которая вызывается в начале каждого метода с его именем; ненулевая ошибка
// возвращается вызывающему. Безопасно вызывать во время работы сервиса.
func (c *Client) SetHook(f func(method string) error) {
	c.mu.Lock()
	c.hook = f
	c.mu.Unlock()
}

var _ rpcclient.API = (*Client)(nil)

// Client создаёт новое соединение.
func (s *Server) Client() *Client { return &Client{srv: s} }

// Calls возвращает имена вызванных методов.
func (c *Client) Calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

// ResetCalls очищает журнал вызовов.
func (c *Client) ResetCalls() {
	c.mu.Lock()
	c.calls = nil
	c.mu.Unlock()
}

// Count возвращает число вызовов метода.
func (c *Client) Count(method string) int {
	n := 0
	for _, m := range c.Calls() {
		if m == method {
			n++
		}
	}
	return n
}

// Token — текущая сессия соединения (нужна тестам для отзыва).
func (c *Client) Token() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

func (c *Client) enter(ctx context.Context, method string) error {
	c.mu.Lock()
	c.calls = append(c.calls, method)
	hook := c.hook
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return apperr.New(apperr.Timeout)
	}
	if hook != nil {
		if err := hook(method); err != nil {
			return err
		}
	}
	c.srv.mu.Lock()
	down := c.srv.down
	c.srv.mu.Unlock()
	if down {
		return apperr.New(apperr.ServerUnavailable)
	}
	return nil
}

// user возвращает логин сессии; вызывается под s.mu.
func (c *Client) user() (string, error) {
	c.mu.Lock()
	t := c.token
	c.mu.Unlock()
	u, ok := c.srv.tokens[t]
	if !ok || t == "" {
		return "", apperr.New(apperr.SessionExpired)
	}
	return u, nil
}

func (c *Client) SetToken(token string) {
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
}

func (c *Client) Close() error { return nil }

func (c *Client) Health(ctx context.Context) error { return c.enter(ctx, "Health") }

func validLogin(l string) bool {
	if len(l) < 3 || len(l) > 32 {
		return false
	}
	for _, r := range l {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
			return false
		}
	}
	return true
}

func (c *Client) Register(ctx context.Context, login, password string) error {
	if err := c.enter(ctx, "Register"); err != nil {
		return err
	}
	s := c.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validLogin(login) {
		return apperr.WithField(apperr.InvalidLogin, "login")
	}
	if len(password) < 8 || len(password) > 128 {
		return apperr.WithField(apperr.InvalidPassword, "password")
	}
	k := strings.ToLower(login)
	if _, ok := s.users[k]; ok {
		return apperr.New(apperr.LoginTaken)
	}
	s.users[k], s.names[k] = password, login
	return nil
}

func (c *Client) Login(ctx context.Context, login, password string) (rpcclient.Session, error) {
	if err := c.enter(ctx, "Login"); err != nil {
		return rpcclient.Session{}, err
	}
	s := c.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	k := strings.ToLower(login)
	if pw, ok := s.users[k]; !ok || pw != password {
		return rpcclient.Session{}, apperr.New(apperr.InvalidCredentials)
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	s.tokens[tok] = k
	return rpcclient.Session{Token: tok, UserID: "id-" + k, Login: s.names[k]}, nil
}

func (c *Client) Logout(ctx context.Context) error {
	if err := c.enter(ctx, "Logout"); err != nil {
		return err
	}
	s := c.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := c.user(); err != nil {
		return err
	}
	delete(s.tokens, c.Token())
	return nil
}

func (c *Client) SendContactRequest(ctx context.Context, login string) (bool, error) {
	if err := c.enter(ctx, "SendContactRequest"); err != nil {
		return false, err
	}
	s := c.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	me, err := c.user()
	if err != nil {
		return false, err
	}
	to := strings.ToLower(login)
	switch {
	case to == me:
		return false, apperr.New(apperr.SelfRequest)
	case s.users[to] == "":
		return false, apperr.New(apperr.UserNotFound)
	case s.contacts[pair(me, to)]:
		return false, apperr.New(apperr.AlreadyContact)
	case s.requests[[2]string{me, to}]:
		return false, apperr.New(apperr.DuplicateRequest)
	case s.requests[[2]string{to, me}]:
		delete(s.requests, [2]string{to, me})
		s.contacts[pair(me, to)] = true
		return true, nil
	}
	s.requests[[2]string{me, to}] = true
	return false, nil
}

func (c *Client) answer(ctx context.Context, method, login string, accept bool) error {
	if err := c.enter(ctx, method); err != nil {
		return err
	}
	s := c.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	me, err := c.user()
	if err != nil {
		return err
	}
	from := strings.ToLower(login)
	if !s.requests[[2]string{from, me}] {
		return apperr.New(apperr.RequestNotFound)
	}
	delete(s.requests, [2]string{from, me})
	if accept {
		s.contacts[pair(me, from)] = true
	}
	return nil
}

func (c *Client) AcceptContact(ctx context.Context, login string) error {
	return c.answer(ctx, "AcceptContact", login, true)
}

func (c *Client) RejectContact(ctx context.Context, login string) error {
	return c.answer(ctx, "RejectContact", login, false)
}

func (c *Client) RemoveContact(ctx context.Context, login string) error {
	if err := c.enter(ctx, "RemoveContact"); err != nil {
		return err
	}
	s := c.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	me, err := c.user()
	if err != nil {
		return err
	}
	other := strings.ToLower(login)
	if !s.contacts[pair(me, other)] {
		return apperr.New(apperr.ContactNotFound)
	}
	delete(s.contacts, pair(me, other))
	for _, ch := range s.chats {
		if pair(ch.members[0], ch.members[1]) == pair(me, other) && ch.closed.IsZero() {
			ch.closed = s.tick()
			ch.values = map[string][]byte{}
			s.dropChatLocked(ch.id)
		}
	}
	return nil
}

func (c *Client) ListContacts(ctx context.Context) (rpcclient.Contacts, error) {
	if err := c.enter(ctx, "ListContacts"); err != nil {
		return rpcclient.Contacts{}, err
	}
	s := c.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	me, err := c.user()
	if err != nil {
		return rpcclient.Contacts{}, err
	}
	out := rpcclient.Contacts{Contacts: []string{}, Incoming: []string{}, Outgoing: []string{}}
	for p := range s.contacts {
		switch me {
		case p[0]:
			out.Contacts = append(out.Contacts, s.names[p[1]])
		case p[1]:
			out.Contacts = append(out.Contacts, s.names[p[0]])
		}
	}
	for r := range s.requests {
		if r[1] == me {
			out.Incoming = append(out.Incoming, s.names[r[0]])
		}
		if r[0] == me {
			out.Outgoing = append(out.Outgoing, s.names[r[1]])
		}
	}
	sort.Strings(out.Contacts)
	sort.Strings(out.Incoming)
	sort.Strings(out.Outgoing)
	return out, nil
}

func (s *Server) view(ch *chat, me string) rpcclient.Chat {
	peer := ch.members[0]
	if peer == me {
		peer = ch.members[1]
	}
	return rpcclient.Chat{
		ID: ch.id, Params: ch.params, Creator: s.names[ch.creator],
		Self:      rpcclient.Participant{Login: s.names[me], Connected: ch.conn[me], Published: ch.values[me] != nil},
		Peer:      rpcclient.Participant{Login: s.names[peer], Connected: ch.conn[peer], Published: ch.values[peer] != nil},
		CreatedAt: ch.created, ClosedAt: ch.closed,
	}
}

func (c *Client) CreateChat(ctx context.Context, peer string, p rpcclient.ChatParams) (rpcclient.Chat, error) {
	if err := c.enter(ctx, "CreateChat"); err != nil {
		return rpcclient.Chat{}, err
	}
	s := c.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	me, err := c.user()
	if err != nil {
		return rpcclient.Chat{}, err
	}
	other := strings.ToLower(peer)
	switch {
	case other == me:
		return rpcclient.Chat{}, apperr.New(apperr.SelfChat)
	case !s.contacts[pair(me, other)]:
		return rpcclient.Chat{}, apperr.New(apperr.NotContact)
	case p.Algorithm != 1 && p.Algorithm != 2:
		return rpcclient.Chat{}, apperr.WithField(apperr.InvalidChatParams, "algorithm")
	case p.KeyBits != 128 && p.KeyBits != 192 && p.KeyBits != 256:
		return rpcclient.Chat{}, apperr.WithField(apperr.InvalidChatParams, "key_bits")
	case p.Mode < 1 || p.Mode > 7:
		return rpcclient.Chat{}, apperr.WithField(apperr.InvalidChatParams, "cipher_mode")
	case p.Padding < 1 || p.Padding > 4:
		return rpcclient.Chat{}, apperr.WithField(apperr.InvalidChatParams, "padding_mode")
	}
	ch := &chat{params: p, creator: me, members: [2]string{me, other}, conn: map[string]bool{me: true},
		values: map[string][]byte{}, created: s.tick()}
	_, _ = rand.Read(ch.id[:])
	s.chats[ch.id] = ch
	return s.view(ch, me), nil
}

func (c *Client) ListChats(ctx context.Context) ([]rpcclient.Chat, error) {
	if err := c.enter(ctx, "ListChats"); err != nil {
		return nil, err
	}
	s := c.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	me, err := c.user()
	if err != nil {
		return nil, err
	}
	out := []rpcclient.Chat{}
	for _, ch := range s.chats {
		if ch.closed.IsZero() && (ch.members[0] == me || ch.members[1] == me) {
			out = append(out, s.view(ch, me))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// member возвращает чат пользователя; вызывается под s.mu.
func (c *Client) member(id [16]byte) (*chat, string, error) {
	me, err := c.user()
	if err != nil {
		return nil, "", err
	}
	ch := c.srv.chats[id]
	if ch == nil || (ch.members[0] != me && ch.members[1] != me) {
		return nil, "", apperr.New(apperr.ChatNotFound)
	}
	return ch, me, nil
}

func (c *Client) chatOp(ctx context.Context, method string, id [16]byte, f func(ch *chat, me string) error) (rpcclient.Chat, error) {
	if err := c.enter(ctx, method); err != nil {
		return rpcclient.Chat{}, err
	}
	s := c.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, me, err := c.member(id)
	if err != nil {
		return rpcclient.Chat{}, err
	}
	if f != nil {
		if err := f(ch, me); err != nil {
			return rpcclient.Chat{}, err
		}
	}
	return s.view(ch, me), nil
}

func (c *Client) GetChat(ctx context.Context, id [16]byte) (rpcclient.Chat, error) {
	return c.chatOp(ctx, "GetChat", id, nil)
}

func (c *Client) ConnectChat(ctx context.Context, id [16]byte) (rpcclient.Chat, error) {
	return c.chatOp(ctx, "ConnectChat", id, func(ch *chat, me string) error {
		if !ch.closed.IsZero() {
			return apperr.New(apperr.ChatClosed)
		}
		ch.conn[me] = true
		return nil
	})
}

func (c *Client) DisconnectChat(ctx context.Context, id [16]byte) (rpcclient.Chat, error) {
	return c.chatOp(ctx, "DisconnectChat", id, func(ch *chat, me string) error {
		if ch.closed.IsZero() {
			ch.conn[me] = false
		}
		return nil
	})
}

func (c *Client) CloseChat(ctx context.Context, id [16]byte) (rpcclient.Chat, error) {
	return c.chatOp(ctx, "CloseChat", id, func(ch *chat, me string) error {
		if ch.closed.IsZero() {
			ch.closed = c.srv.tick()
			ch.values = map[string][]byte{}
			ch.conn = map[string]bool{}
			c.srv.dropChatLocked(id)
		}
		return nil
	})
}

func (c *Client) PublishPublicValue(ctx context.Context, id [16]byte, value []byte) error {
	_, err := c.chatOp(ctx, "PublishPublicValue", id, func(ch *chat, me string) error {
		switch {
		case !ch.closed.IsZero():
			return apperr.New(apperr.ChatClosed)
		case !ch.conn[me]:
			return apperr.New(apperr.NotConnected)
		case len(value) != 256:
			return apperr.New(apperr.Internal)
		case ch.values[me] != nil && !slices.Equal(ch.values[me], value):
			return apperr.New(apperr.KeyLost)
		case ch.values[me] == nil:
			ch.values[me] = slices.Clone(value)
			c.srv.published++
		}
		return nil
	})
	return err
}

func (c *Client) GetPeerPublicValue(ctx context.Context, id [16]byte) ([]byte, bool, error) {
	var out []byte
	_, err := c.chatOp(ctx, "GetPeerPublicValue", id, func(ch *chat, me string) error {
		if !ch.closed.IsZero() {
			return apperr.New(apperr.ChatClosed)
		}
		peer := ch.members[0]
		if peer == me {
			peer = ch.members[1]
		}
		out = slices.Clone(ch.values[peer])
		return nil
	})
	return out, out != nil, err
}
