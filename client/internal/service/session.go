package service

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"os"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/keyagree"
	"cryptochat/client/internal/profile"
	"cryptochat/client/internal/rpcclient"
)

// Правила сервера для логина и пароля: проверяются до отправки.
const (
	minLogin    = 3
	maxLogin    = 32
	minPassword = 8
	maxPassword = 128
)

func validLogin(login string) bool {
	if len(login) < minLogin || len(login) > maxLogin {
		return false
	}
	for i := 0; i < len(login); i++ {
		c := login[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.', c == '-':
		default:
			return false
		}
	}
	return true
}

func validateCredentials(login, password string) error {
	if !validLogin(login) {
		return apperr.WithField(apperr.InvalidLogin, "login")
	}
	if len(password) < minPassword || len(password) > maxPassword {
		return apperr.WithField(apperr.InvalidPassword, "password")
	}
	return nil
}

// settingsLocked загружает настройки при первом обращении; вызывается под s.mu. Возвращает true, если
// файл был повреждён и настройки сброшены.
func (s *Service) settingsLocked() bool {
	if s.loaded {
		return false
	}
	st, err := profile.Load(s.cfg.Root)
	s.settings, s.loaded = st, true
	if err != nil {
		s.log.Printf("настройки: файл повреждён, используются значения по умолчанию")
		return true
	}
	return false
}

// GetSettings возвращает настройки; повреждённый файл заменяется значениями по умолчанию с уведомлением.
func (s *Service) GetSettings() (profile.Settings, error) {
	s.mu.Lock()
	reset := s.settingsLocked()
	st := s.settings
	s.mu.Unlock()
	if reset {
		s.notice(NoticeWarning, apperr.SettingsInvalid, "")
	}
	st.RecentLogins = append([]string(nil), st.RecentLogins...)
	return st, nil
}

// certDigest — хеш содержимого файла сертификата; файл не читается — нулевое значение.
func certDigest(path string) [sha256.Size]byte {
	if path == "" {
		return [sha256.Size]byte{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return [sha256.Size]byte{}
	}
	return sha256.Sum256(raw)
}

func checkCertificate(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil || !x509.NewCertPool().AppendCertsFromPEM(raw) {
		return apperr.New(apperr.CertFile)
	}
	return nil
}

// SaveSettings проверяет и сохраняет адрес и сертификат. Смена адреса, пути к сертификату или содержимого
// файла по тому же пути пересоздаёт соединение; открытая сессия при этом завершается, потому что профиль
// привязан к адресу.
func (s *Service) SaveSettings(in profile.Settings) (profile.Settings, error) {
	if err := profile.ValidateAddress(in.ServerAddress); err != nil {
		return profile.Settings{}, err
	}
	if in.CertificatePath != "" {
		if err := checkCertificate(in.CertificatePath); err != nil {
			return profile.Settings{}, err
		}
	}
	s.mu.Lock()
	s.settingsLocked()
	next := s.settings
	changed := next.ServerAddress != in.ServerAddress || next.CertificatePath != in.CertificatePath ||
		(s.api != nil && certDigest(in.CertificatePath) != s.certSum)
	next.ServerAddress, next.CertificatePath = in.ServerAddress, in.CertificatePath
	sess := s.sess
	s.mu.Unlock()

	if err := profile.Save(s.cfg.Root, next); err != nil {
		s.log.Printf("запись настроек: %v", err)
		return profile.Settings{}, apperr.New(apperr.Internal)
	}
	if changed && sess != nil {
		_ = s.Logout()
	}
	s.mu.Lock()
	s.settings = next
	var old rpcclient.API
	if changed {
		old, s.api = s.api, nil
		s.connection = Connecting
	}
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	s.publish()
	return s.GetSettings()
}

// dialLocked открывает соединение по текущим настройкам и возвращает хеш сертификата, с которым оно
// открыто; вызывается под s.mu.
func (s *Service) dialLocked() (rpcclient.API, [sha256.Size]byte, error) {
	s.settingsLocked()
	sum := certDigest(s.settings.CertificatePath)
	api, err := s.cfg.Dial(s.settings.ServerAddress, s.settings.CertificatePath)
	if err != nil {
		if _, ok := apperr.As(err); !ok {
			err = apperr.New(apperr.Internal)
		}
		return nil, sum, err
	}
	return api, sum, nil
}

// conn возвращает соединение с сервером по текущим настройкам. Вне сессии соединение пересоздаётся,
// если файл сертификата по тому же пути заменён; соединение открытой сессии не меняется.
func (s *Service) conn() (rpcclient.API, error) {
	s.mu.Lock()
	var old rpcclient.API
	if s.api != nil {
		s.settingsLocked()
		if s.sess != nil || certDigest(s.settings.CertificatePath) == s.certSum {
			api := s.api
			s.mu.Unlock()
			return api, nil
		}
		old, s.api = s.api, nil
	}
	api, sum, err := s.dialLocked()
	if err == nil {
		s.api, s.certSum = api, sum
	}
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return api, err
}

// CheckServer проверяет связь с сервером и сертификат. Если файл сертификата заменён, а сессия открыта,
// проверка идёт по отдельному новому соединению, которое затем закрывается.
func (s *Service) CheckServer() error {
	s.mu.Lock()
	sess := s.sess
	s.settingsLocked()
	stale := sess != nil && s.api != nil && certDigest(s.settings.CertificatePath) != s.certSum
	var (
		api rpcclient.API
		err error
	)
	if stale {
		api, _, err = s.dialLocked()
	}
	s.mu.Unlock()
	if stale {
		if err != nil {
			return err
		}
		defer func() { _ = api.Close() }()
		return s.observe(sess, api.Health(context.Background()))
	}
	api, err = s.conn()
	if err != nil {
		return err
	}
	return s.observe(sess, api.Health(context.Background()))
}

// Register создаёт учётную запись.
func (s *Service) Register(login, password string) error {
	if err := validateCredentials(login, password); err != nil {
		return err
	}
	api, err := s.conn()
	if err != nil {
		return err
	}
	return s.observe(nil, api.Register(context.Background(), login, password))
}

// revoke отзывает сессию, которую нельзя использовать (профиль занят или повреждён).
func (s *Service) revoke(api rpcclient.API, token string) {
	api.SetToken(token)
	if err := api.Logout(context.Background()); err != nil {
		s.log.Printf("отзыв сессии: %s", apperr.CodeOf(err))
	}
	api.SetToken("")
}

// Login входит, открывает профиль «адрес + пользователь», выполняет первую синхронизацию и запускает
// цикл. Токен хранится только в памяти соединения.
func (s *Service) Login(login, password string) (Snapshot, error) {
	if err := validateCredentials(login, password); err != nil {
		return Snapshot{}, err
	}
	// Вход и выход выполняются по одному: иначе два одновременных входа заменили бы сессию друг друга,
	// а цикл и база первой остались бы без владельца.
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if _, err := s.current(); err == nil {
		s.logoutLocked()
	}
	api, err := s.conn()
	if err != nil {
		return Snapshot{}, err
	}
	r, err := api.Login(context.Background(), login, password)
	if err := s.observe(nil, err); err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	address := s.settings.ServerAddress
	s.mu.Unlock()

	db, err := s.cfg.OpenStore(profile.DBPath(s.cfg.Root, address, r.UserID))
	if err != nil {
		s.revoke(api, r.Token)
		if _, ok := apperr.As(err); !ok {
			err = apperr.New(apperr.Internal)
		}
		return Snapshot{}, err
	}
	if err := db.EnsureProfile(address, r.UserID, r.Login); err != nil {
		_ = db.Close()
		s.revoke(api, r.Token)
		return Snapshot{}, err
	}

	s.mu.Lock()
	s.settings.AddRecentLogin(r.Login)
	st := s.settings
	s.mu.Unlock()
	if err := profile.Save(s.cfg.Root, st); err != nil {
		s.log.Printf("запись настроек: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sess := &session{
		login: r.Login, userID: r.UserID, address: address, db: db, api: api,
		ctx: ctx, cancel: cancel, kick: make(chan struct{}, 1), done: make(chan struct{}),
		chats: map[[16]byte]*chatState{},
	}
	// Пока сервер не ответил, показываются чаты из локальной базы.
	if known, err := db.KnownActiveChats(); err == nil {
		for _, c := range known {
			sess.chats[c.ID] = &chatState{local: c, key: keyagree.LocalState(db, c.ID)}
		}
	}
	api.SetToken(r.Token)
	s.startEngine(sess)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		sess.engine.Stop()
		cancel()
		_ = db.Close()
		return Snapshot{}, apperr.New(apperr.Internal)
	}
	s.sess = sess
	s.mu.Unlock()

	s.syncNow(sess)
	s.wg.Add(1)
	go s.loop(sess)
	s.publish()
	return s.Snapshot(), nil
}

// Logout завершает сессию. Ошибка сервера не мешает: локальная сессия завершается всегда.
func (s *Service) Logout() error {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	s.logoutLocked()
	return nil
}

// logoutLocked завершает текущую сессию; вызывается под s.loginMu.
func (s *Service) logoutLocked() {
	sess, err := s.current()
	if err != nil {
		return
	}
	if err := sess.api.Logout(context.Background()); err != nil {
		s.log.Printf("выход на сервере: %s", apperr.CodeOf(err))
	}
	s.endSession(sess, EndedLoggedOut, true)
}
