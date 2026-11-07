// Пакет service — логика клиента без окна: сессия, контакты, чаты, синхронизация с сервером,
// согласование ключа и снимок состояния для интерфейса.
package service

import (
	"context"
	"crypto/sha256"
	"log"
	"os"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/ciphers"
	"cryptochat/client/internal/keyagree"
	"cryptochat/client/internal/localdb"
	"cryptochat/client/internal/profile"
	"cryptochat/client/internal/rpcclient"
)

// DefaultInterval — период синхронизации с сервером.
const DefaultInterval = 2 * time.Second

// Config — зависимости сервиса. Пустые поля получают рабочие значения.
type Config struct {
	Root      string                                                // корень данных; пусто — profile.Root()
	Dial      func(address, certPath string) (rpcclient.API, error) // соединение с сервером
	OpenStore func(path string) (*localdb.DB, error)                // открытие базы профиля
	Interval  time.Duration
	Logger    *log.Logger
}

// Service — состояние клиента. Методы безопасны для одновременного вызова.
type Service struct {
	cfg Config
	log *log.Logger

	mu         sync.Mutex
	settings   profile.Settings
	loaded     bool
	api        rpcclient.API
	certSum    [sha256.Size]byte // хеш файла сертификата, с которым открыто api
	sess       *session
	snap       Snapshot
	connection string
	emitters   []Emitter
	closed     bool

	loginMu sync.Mutex // один вход или выход за раз
	syncMu  sync.Mutex // одна синхронизация за раз
	keyMu   sync.Mutex // одно согласование за раз: секрет не создаётся дважды
	wg      sync.WaitGroup
}

// chatState — известный активный чат текущей сессии.
type chatState struct {
	local         localdb.Chat
	selfConnected bool
	peerConnected bool
	key           keyagree.State
}

// session — вход пользователя. Поля после login меняются только под Service.mu.
type session struct {
	login   string
	userID  string
	address string
	db      *localdb.DB
	api     rpcclient.API

	ctx    context.Context
	cancel context.CancelFunc
	kick   chan struct{}
	done   chan struct{}
	ended  bool

	contacts rpcclient.Contacts
	chats    map[[16]byte]*chatState
	// closed — чаты, закрытые на устройстве за эту сессию. Синхронизация, начатая до закрытия, не
	// возвращает их в список и не создаёт для них секрет. Меняется под Service.keyMu и Service.mu.
	closed map[[16]byte]bool

	storageFaults int  // число ошибок записи в базу профиля за сессию
	storageAlert  bool // об ошибке записи уже сообщено; сбрасывается после цикла без ошибок
}

// New создаёт сервис.
func New(cfg Config) *Service {
	if cfg.Root == "" {
		if r, err := profile.Root(); err == nil {
			cfg.Root = r
		}
	}
	if cfg.Dial == nil {
		cfg.Dial = func(address, cert string) (rpcclient.API, error) { return rpcclient.Dial(address, cert) }
	}
	if cfg.OpenStore == nil {
		cfg.OpenStore = localdb.Open
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(os.Stderr, "cryptochat: ", log.LstdFlags)
	}
	s := &Service{cfg: cfg, log: cfg.Logger, connection: Connecting}
	s.snap = Snapshot{Connection: Connecting, Contacts: []string{}, Incoming: []string{}, Outgoing: []string{}, Chats: []ChatInfo{}}
	return s
}

// Subscribe добавляет получателя событий.
func (s *Service) Subscribe(e Emitter) {
	s.mu.Lock()
	s.emitters = append(s.emitters, e)
	s.mu.Unlock()
}

func (s *Service) emit(event string, data any) {
	s.mu.Lock()
	list := append([]Emitter(nil), s.emitters...)
	s.mu.Unlock()
	for _, e := range list {
		s.protect("отправка события", func() { e.Emit(event, data) })
	}
}

func (s *Service) notice(kind, code, message string) {
	if message == "" {
		message = apperr.New(code).Message
	}
	s.emit(EventNotice, Notice{Kind: kind, Code: code, Message: message})
}

// protect выполняет f и превращает панику в уведомление INTERNAL. Значение паники в журнал не пишется:
// в нём могут оказаться данные чата.
func (s *Service) protect(where string, f func()) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
			s.log.Printf("внутренняя ошибка (%s)\n%s", where, debug.Stack())
			if where != "отправка события" {
				s.notice(NoticeError, apperr.Internal, "")
			}
		}
	}()
	f()
	return true
}

// goSafe запускает фоновую задачу с перехватом паники.
func (s *Service) goSafe(where string, f func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.protect(where, f)
	}()
}

// Close завершает сессию без обращения к серверу и останавливает фоновые задачи.
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	sess := s.sess
	api := s.api
	s.mu.Unlock()
	if sess != nil {
		s.endSession(sess, "", true)
	}
	s.wg.Wait()
	if api != nil {
		_ = api.Close()
	}
}

// Snapshot возвращает текущий снимок.
func (s *Service) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap
}

// buildLocked собирает снимок из состояния; вызывается под s.mu.
func (s *Service) buildLocked() Snapshot {
	snap := Snapshot{Connection: s.connection, Contacts: []string{}, Incoming: []string{}, Outgoing: []string{}, Chats: []ChatInfo{}}
	sess := s.sess
	if sess == nil {
		return snap
	}
	snap.Session = &SessionInfo{Login: sess.login, UserID: sess.userID}
	snap.Contacts = sortedLogins(sess.contacts.Contacts)
	snap.Incoming = sortedLogins(sess.contacts.Incoming)
	snap.Outgoing = sortedLogins(sess.contacts.Outgoing)
	for _, c := range sess.chats {
		snap.Chats = append(snap.Chats, chatInfo(c))
	}
	sort.Slice(snap.Chats, func(i, j int) bool {
		if !snap.Chats[i].CreatedAt.Equal(snap.Chats[j].CreatedAt) {
			return snap.Chats[i].CreatedAt.After(snap.Chats[j].CreatedAt)
		}
		return string(snap.Chats[i].ID[:]) < string(snap.Chats[j].ID[:])
	})
	return snap
}

func chatInfo(c *chatState) ChatInfo {
	l := c.local
	info := ChatInfo{
		ID: l.ID, Peer: l.Peer, Creator: l.Creator, Algorithm: l.Algorithm, KeyBits: l.KeyBits, Mode: l.Mode,
		Padding: l.Padding, CreatedAt: l.CreatedAt, SelfConnected: c.selfConnected, PeerConnected: c.peerConnected,
		KeyState: c.key,
	}
	if st := ciphers.Info(l.Algorithm); st.Substitute || !st.Available {
		info.AlgorithmNote = st.Note
	}
	return info
}

// publish пересобирает снимок и отправляет его, если он изменился.
func (s *Service) publish() {
	s.mu.Lock()
	next := s.buildLocked()
	if sameContent(next, s.snap) {
		s.mu.Unlock()
		return
	}
	next.Version = s.snap.Version + 1
	s.snap = next
	s.mu.Unlock()
	s.emit(EventSnapshot, next)
}

// setConnection меняет состояние связи и сообщает о переходах.
func (s *Service) setConnection(state string) {
	s.mu.Lock()
	prev := s.connection
	s.connection = state
	inSession := s.sess != nil
	s.mu.Unlock()
	if prev == state {
		return
	}
	if inSession && state == Offline {
		s.notice(NoticeWarning, apperr.ServerUnavailable, "Нет связи с сервером: показано последнее известное состояние")
	}
	if inSession && state == Online && prev == Offline {
		s.notice(NoticeInfo, "CONNECTION_RESTORED", "Связь с сервером восстановлена")
	}
	s.publish()
}

// observe учитывает результат вызова сервера: состояние связи и отзыв сессии.
func (s *Service) observe(sess *session, err error) error {
	switch apperr.CodeOf(err) {
	case "":
		s.setConnection(Online)
	case apperr.ServerUnavailable, apperr.Timeout, apperr.CertInvalid:
		s.setConnection(Offline)
	case apperr.SessionExpired:
		if sess != nil {
			s.endSession(sess, EndedExpired, false)
		}
	case apperr.ServerBusy:
		// Сервер отвечает, но временно не может выполнить запрос: сессия и связь сохраняются.
		s.setConnection(Online)
	default:
		// Ответ сервера с отказом по существу: связь есть.
		if _, ok := apperr.As(err); ok && err != nil {
			s.setConnection(Online)
		}
	}
	return err
}

// storageFault сообщает об ошибке записи в базу профиля: одно уведомление на серию сбоев.
func (s *Service) storageFault(sess *session) {
	s.mu.Lock()
	sess.storageFaults++
	first := !sess.storageAlert
	sess.storageAlert = true
	s.mu.Unlock()
	if first {
		s.notice(NoticeError, apperr.ProfileStorage, "Не удалось записать данные профиля: "+sess.db.Dir())
	}
}

// storageFaultCount возвращает число ошибок записи; по нему цикл синхронизации узнаёт, прошёл ли он
// без сбоев.
func (s *Service) storageFaultCount(sess *session) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sess.storageFaults
}

// storageRecovered завершает серию сбоев, если с начала цикла новых ошибок записи не было: следующий
// сбой снова даст уведомление.
func (s *Service) storageRecovered(sess *session, before int) {
	s.mu.Lock()
	if sess.storageFaults == before {
		sess.storageAlert = false
	}
	s.mu.Unlock()
}

// current возвращает активную сессию.
func (s *Service) current() (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sess == nil {
		return nil, apperr.New(apperr.NotLoggedIn)
	}
	return s.sess, nil
}

// endSession завершает сессию: останавливает цикл, закрывает базу профиля и сообщает интерфейсу.
// code пуст — без события (закрытие приложения). wait — дождаться остановки цикла; из самого цикла false.
func (s *Service) endSession(sess *session, code string, wait bool) {
	s.mu.Lock()
	if sess.ended {
		s.mu.Unlock()
		return
	}
	sess.ended = true
	if s.sess == sess {
		s.sess = nil
	}
	s.mu.Unlock()
	sess.api.SetToken("")
	sess.cancel()
	closeDB := func() {
		<-sess.done
		if err := sess.db.Close(); err != nil {
			s.log.Printf("закрытие базы профиля: %v", err)
		}
	}
	if wait {
		closeDB()
	} else {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			closeDB()
		}()
	}
	s.publish()
	if code != "" {
		s.emit(EventSessionEnded, SessionEnded{Code: code, Login: sess.login})
	}
}
