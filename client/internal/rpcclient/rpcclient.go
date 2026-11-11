// Пакет rpcclient — соединение клиента с сервером по gRPC: TLS 1.3 с сертификатом из файла, сессия в
// метаданных, сроки вызовов и перевод ошибок в коды клиента.
package rpcclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "cryptochat/client/internal/api/v1"
	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/profile"
	"cryptochat/core/cipher"
)

// Сроки вызовов. Вход и регистрация дольше: сервер вычисляет argon2id.
const (
	CallTimeout  = 5 * time.Second
	LoginTimeout = 10 * time.Second
	probeTimeout = 3 * time.Second
)

// ChatParams — параметры чата; Mode и Padding — номера enum контракта.
type ChatParams struct {
	Algorithm cipher.Algorithm
	KeyBits   int
	Mode      int
	Padding   int
}

// Participant — участник чата с точки зрения сервера.
type Participant struct {
	Login     string
	Connected bool
	Published bool
}

// Chat — чат в ответе сервера.
type Chat struct {
	ID        [16]byte
	Params    ChatParams
	Creator   string
	Self      Participant
	Peer      Participant
	CreatedAt time.Time
	ClosedAt  time.Time // нулевое значение — чат активен
}

// Closed сообщает, что чат закрыт.
func (c Chat) Closed() bool { return !c.ClosedAt.IsZero() }

// Session — результат входа.
type Session struct {
	Token  string
	UserID string
	Login  string
}

// Contacts — списки контактов и запросов.
type Contacts struct {
	Contacts []string
	Incoming []string
	Outgoing []string
}

// API — вызовы сервера, нужные клиенту. Ошибки — *apperr.Error.
type API interface {
	Register(ctx context.Context, login, password string) error
	Login(ctx context.Context, login, password string) (Session, error)
	Logout(ctx context.Context) error
	SetToken(token string)
	Health(ctx context.Context) error

	SendContactRequest(ctx context.Context, login string) (bool, error)
	AcceptContact(ctx context.Context, login string) error
	RejectContact(ctx context.Context, login string) error
	RemoveContact(ctx context.Context, login string) error
	ListContacts(ctx context.Context) (Contacts, error)

	CreateChat(ctx context.Context, peer string, p ChatParams) (Chat, error)
	ListChats(ctx context.Context) ([]Chat, error)
	GetChat(ctx context.Context, id [16]byte) (Chat, error)
	ConnectChat(ctx context.Context, id [16]byte) (Chat, error)
	DisconnectChat(ctx context.Context, id [16]byte) (Chat, error)
	CloseChat(ctx context.Context, id [16]byte) (Chat, error)

	PublishPublicValue(ctx context.Context, id [16]byte, value []byte) error
	GetPeerPublicValue(ctx context.Context, id [16]byte) ([]byte, bool, error)

	// SendFragment передаёт сериализованный фрагмент; успех означает, что брокер его принял.
	SendFragment(ctx context.Context, chatID [16]byte, wire []byte) error
	// OpenReceive открывает поток приёма чата и возвращает его после ответа opened.
	OpenReceive(ctx context.Context, chatID [16]byte) (ReceiveStream, error)

	Close() error
}

// Conn — соединение с сервером. Методы безопасны для одновременного вызова.
type Conn struct {
	address string
	tlsConf *tls.Config
	cc      *grpc.ClientConn

	mu    sync.RWMutex
	token string

	accounts apiv1.AccountsClient
	contacts apiv1.ContactsClient
	chats    apiv1.ChatsClient
	keys     apiv1.KeyExchangeClient
	health   healthpb.HealthClient
	delivery apiv1.DeliveryClient
}

var _ API = (*Conn)(nil)

// Dial создаёт соединение. Сеть не используется до первого вызова, поэтому недоступный сервер здесь
// ещё не ошибка.
func Dial(address, certPath string) (*Conn, error) {
	if err := profile.ValidateAddress(address); err != nil {
		return nil, err
	}
	pemData, err := os.ReadFile(certPath)
	if err != nil || certPath == "" {
		return nil, apperr.New(apperr.CertFile)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemData) {
		return nil, apperr.New(apperr.CertFile)
	}
	c := &Conn{address: address, tlsConf: &tls.Config{
		RootCAs:    pool,
		ServerName: profile.Host(address),
		MinVersion: tls.VersionTLS13,
	}}
	params := grpc.ConnectParams{
		Backoff:           backoff.Config{BaseDelay: 500 * time.Millisecond, Multiplier: 1.6, Jitter: 0.2, MaxDelay: 2 * time.Second},
		MinConnectTimeout: 3 * time.Second,
	}
	cc, err := grpc.NewClient(address,
		grpc.WithTransportCredentials(credentials.NewTLS(c.tlsConf.Clone())),
		grpc.WithConnectParams(params),
		grpc.WithUnaryInterceptor(c.authorize),
	)
	if err != nil {
		return nil, apperr.New(apperr.InvalidAddress)
	}
	c.cc = cc
	c.accounts = apiv1.NewAccountsClient(cc)
	c.contacts = apiv1.NewContactsClient(cc)
	c.chats = apiv1.NewChatsClient(cc)
	c.keys = apiv1.NewKeyExchangeClient(cc)
	c.health = healthpb.NewHealthClient(cc)
	c.delivery = apiv1.NewDeliveryClient(cc)
	return c, nil
}

// authorize добавляет заголовок сессии, если она есть.
func (c *Conn) authorize(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn,
	invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	c.mu.RLock()
	token := c.token
	c.mu.RUnlock()
	if token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	}
	return invoker(ctx, method, req, reply, cc, opts...)
}

// SetToken задаёт сессию; пустая строка — без сессии. Токен хранится только в памяти.
func (c *Conn) SetToken(token string) {
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
}

// Close закрывает соединение.
func (c *Conn) Close() error { return c.cc.Close() }

// Classify переводит ошибку вызова в ошибку клиента. Нет связи — пробное TLS-подключение отличает
// неподходящий сертификат от недоступного сервера.
func (c *Conn) Classify(err error) error {
	if err == nil {
		return nil
	}
	e := apperr.FromStatus(err)
	if e.Code == apperr.ServerUnavailable && c.certRejected() {
		return apperr.New(apperr.CertInvalid)
	}
	return e
}

func (c *Conn) certRejected() bool {
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: probeTimeout}, Config: c.tlsConf.Clone()}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", c.address)
	if err == nil {
		_ = conn.Close()
		return false
	}
	var ua x509.UnknownAuthorityError
	var he x509.HostnameError
	var ci x509.CertificateInvalidError
	return errors.As(err, &ua) || errors.As(err, &he) || errors.As(err, &ci)
}

// reconnectWait — сколько вызов ждёт нового соединения, если старое ещё в паузе между попытками.
const reconnectWait = 1500 * time.Millisecond

func call[T any](c *Conn, ctx context.Context, timeout time.Duration, f func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r, err := f(ctx)
	if err != nil && c.notConnected(err) && c.awaitReady(ctx) {
		// Запрос не ушёл: соединения не было. Сервер вернулся раньше, чем закончилась пауза между
		// попытками подключения, поэтому повтор безопасен.
		r, err = f(ctx)
	}
	if err != nil {
		var zero T
		return zero, c.Classify(err)
	}
	return r, nil
}

// notConnected сообщает, что вызов отвергнут без соединения с сервером.
func (c *Conn) notConnected(err error) bool {
	if apperr.FromStatus(err).Code != apperr.ServerUnavailable {
		return false
	}
	st := c.cc.GetState()
	return st == connectivity.TransientFailure || st == connectivity.Connecting || st == connectivity.Idle
}

// awaitReady сбрасывает паузу между попытками подключения и ждёт соединения не дольше reconnectWait.
func (c *Conn) awaitReady(ctx context.Context) bool {
	c.cc.ResetConnectBackoff()
	ctx, cancel := context.WithTimeout(ctx, reconnectWait)
	defer cancel()
	for {
		st := c.cc.GetState()
		switch st {
		case connectivity.Ready:
			return true
		case connectivity.Idle:
			c.cc.Connect()
		}
		if !c.cc.WaitForStateChange(ctx, st) {
			return false
		}
	}
}

// Health проверяет, что сервер отвечает и готов.
func (c *Conn) Health(ctx context.Context) error {
	r, err := call(c, ctx, CallTimeout, func(ctx context.Context) (*healthpb.HealthCheckResponse, error) {
		return c.health.Check(ctx, &healthpb.HealthCheckRequest{})
	})
	if err != nil {
		return err
	}
	if r.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		return apperr.New(apperr.ServerBusy)
	}
	return nil
}

func (c *Conn) Register(ctx context.Context, login, password string) error {
	_, err := call(c, ctx, LoginTimeout, func(ctx context.Context) (*apiv1.RegisterResponse, error) {
		return c.accounts.Register(ctx, &apiv1.RegisterRequest{Login: login, Password: password})
	})
	return err
}

func (c *Conn) Login(ctx context.Context, login, password string) (Session, error) {
	r, err := call(c, ctx, LoginTimeout, func(ctx context.Context) (*apiv1.LoginResponse, error) {
		return c.accounts.Login(ctx, &apiv1.LoginRequest{Login: login, Password: password})
	})
	if err != nil {
		return Session{}, err
	}
	return Session{Token: r.GetToken(), UserID: r.GetUserId(), Login: r.GetLogin()}, nil
}

func (c *Conn) Logout(ctx context.Context) error {
	_, err := call(c, ctx, CallTimeout, func(ctx context.Context) (*apiv1.LogoutResponse, error) {
		return c.accounts.Logout(ctx, &apiv1.LogoutRequest{})
	})
	return err
}

func (c *Conn) SendContactRequest(ctx context.Context, login string) (bool, error) {
	r, err := call(c, ctx, CallTimeout, func(ctx context.Context) (*apiv1.SendContactResponse, error) {
		return c.contacts.SendRequest(ctx, &apiv1.SendContactRequest{Login: login})
	})
	return r.GetBecameContact(), err
}

func (c *Conn) contactCall(ctx context.Context, login string,
	f func(context.Context, *apiv1.ContactLogin, ...grpc.CallOption) (*apiv1.ContactChanged, error)) error {
	_, err := call(c, ctx, CallTimeout, func(ctx context.Context) (*apiv1.ContactChanged, error) {
		return f(ctx, &apiv1.ContactLogin{Login: login})
	})
	return err
}

func (c *Conn) AcceptContact(ctx context.Context, login string) error {
	return c.contactCall(ctx, login, c.contacts.AcceptRequest)
}

func (c *Conn) RejectContact(ctx context.Context, login string) error {
	return c.contactCall(ctx, login, c.contacts.RejectRequest)
}

func (c *Conn) RemoveContact(ctx context.Context, login string) error {
	return c.contactCall(ctx, login, c.contacts.RemoveContact)
}

func logins(refs []*apiv1.UserRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.GetLogin())
	}
	return out
}

func (c *Conn) ListContacts(ctx context.Context) (Contacts, error) {
	r, err := call(c, ctx, CallTimeout, func(ctx context.Context) (*apiv1.ListContactsResponse, error) {
		return c.contacts.ListContacts(ctx, &apiv1.ListContactsRequest{})
	})
	if err != nil {
		return Contacts{}, err
	}
	return Contacts{Contacts: logins(r.GetContacts()), Incoming: logins(r.GetIncoming()), Outgoing: logins(r.GetOutgoing())}, nil
}

// ChatFromProto переводит чат из ответа сервера.
func ChatFromProto(p *apiv1.Chat) Chat {
	var c Chat
	copy(c.ID[:], p.GetChatId())
	c.Params = ChatParams{
		Algorithm: cipher.Algorithm(p.GetParams().GetAlgorithm()),
		KeyBits:   int(p.GetParams().GetKeyBits()),
		Mode:      int(p.GetParams().GetCipherMode()),
		Padding:   int(p.GetParams().GetPaddingMode()),
	}
	c.Creator = p.GetCreator()
	c.Self = Participant{Login: p.GetSelf().GetLogin(), Connected: p.GetSelf().GetConnected(), Published: p.GetSelf().GetPublicValuePublished()}
	c.Peer = Participant{Login: p.GetPeer().GetLogin(), Connected: p.GetPeer().GetConnected(), Published: p.GetPeer().GetPublicValuePublished()}
	c.CreatedAt = timeOf(p.GetCreatedAt())
	c.ClosedAt = timeOf(p.GetClosedAt())
	return c
}

func timeOf(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

func (c *Conn) chatCall(ctx context.Context, f func(context.Context) (*apiv1.ChatResponse, error)) (Chat, error) {
	r, err := call(c, ctx, CallTimeout, f)
	if err != nil {
		return Chat{}, err
	}
	return ChatFromProto(r.GetChat()), nil
}

func (c *Conn) CreateChat(ctx context.Context, peer string, p ChatParams) (Chat, error) {
	return c.chatCall(ctx, func(ctx context.Context) (*apiv1.ChatResponse, error) {
		return c.chats.CreateChat(ctx, &apiv1.CreateChatRequest{PeerLogin: peer, Params: &apiv1.ChatParams{
			Algorithm:   apiv1.Algorithm(p.Algorithm),
			KeyBits:     uint32(p.KeyBits),
			CipherMode:  apiv1.CipherMode(p.Mode),
			PaddingMode: apiv1.PaddingMode(p.Padding),
		}})
	})
}

func (c *Conn) ListChats(ctx context.Context) ([]Chat, error) {
	r, err := call(c, ctx, CallTimeout, func(ctx context.Context) (*apiv1.ListChatsResponse, error) {
		return c.chats.ListChats(ctx, &apiv1.ListChatsRequest{})
	})
	if err != nil {
		return nil, err
	}
	out := make([]Chat, 0, len(r.GetChats()))
	for _, ch := range r.GetChats() {
		out = append(out, ChatFromProto(ch))
	}
	return out, nil
}

func (c *Conn) refCall(ctx context.Context, id [16]byte,
	f func(context.Context, *apiv1.ChatRef, ...grpc.CallOption) (*apiv1.ChatResponse, error)) (Chat, error) {
	return c.chatCall(ctx, func(ctx context.Context) (*apiv1.ChatResponse, error) {
		return f(ctx, &apiv1.ChatRef{ChatId: id[:]})
	})
}

func (c *Conn) GetChat(ctx context.Context, id [16]byte) (Chat, error) {
	return c.refCall(ctx, id, c.chats.GetChat)
}

func (c *Conn) ConnectChat(ctx context.Context, id [16]byte) (Chat, error) {
	return c.refCall(ctx, id, c.chats.ConnectChat)
}

func (c *Conn) DisconnectChat(ctx context.Context, id [16]byte) (Chat, error) {
	return c.refCall(ctx, id, c.chats.DisconnectChat)
}

func (c *Conn) CloseChat(ctx context.Context, id [16]byte) (Chat, error) {
	return c.refCall(ctx, id, c.chats.CloseChat)
}

func (c *Conn) PublishPublicValue(ctx context.Context, id [16]byte, value []byte) error {
	_, err := call(c, ctx, CallTimeout, func(ctx context.Context) (*apiv1.PublishPublicValueResponse, error) {
		return c.keys.PublishPublicValue(ctx, &apiv1.PublishPublicValueRequest{ChatId: id[:], PublicValue: value})
	})
	return err
}

func (c *Conn) GetPeerPublicValue(ctx context.Context, id [16]byte) ([]byte, bool, error) {
	r, err := call(c, ctx, CallTimeout, func(ctx context.Context) (*apiv1.GetPeerPublicValueResponse, error) {
		return c.keys.GetPeerPublicValue(ctx, &apiv1.ChatRef{ChatId: id[:]})
	})
	if err != nil {
		return nil, false, err
	}
	return r.GetPublicValue(), r.GetPublished(), nil
}
