// Пакет accounts реализует регистрацию, вход и сессии.
package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"cryptochat/server/internal/password"
	"cryptochat/server/internal/store"
)

const (
	minLogin    = 3
	maxLogin    = 32
	minPassword = 8
	maxPassword = 128
	sessionTTL  = 24 * time.Hour
	tokenBytes  = 32
)

var (
	ErrInvalidLogin       = errors.New("accounts: недопустимый логин")
	ErrInvalidPassword    = errors.New("accounts: недопустимый пароль")
	ErrInvalidCredentials = errors.New("accounts: неверный логин или пароль")
	ErrUnauthenticated    = errors.New("accounts: нет действующей сессии")
)

// Session — результат входа.
type Session struct {
	Token     string
	ExpiresAt time.Time
	User      store.UserRef
}

// Service — сервис учётных записей.
type Service struct {
	store  store.AccountStore
	hasher *password.Hasher
	now    func() time.Time
	dummy  string // хеш для неизвестного логина: время ответа не раскрывает его существование
}

// New создаёт сервис; фиктивный хеш вычисляется один раз.
func New(ctx context.Context, s store.AccountStore) (*Service, error) {
	return newWithClock(ctx, s, time.Now)
}

func newWithClock(ctx context.Context, s store.AccountStore, now func() time.Time) (*Service, error) {
	h := password.New()
	dummy, err := h.Hash(ctx, "фиктивный пароль для равного времени")
	if err != nil {
		return nil, err
	}
	return &Service{store: s, hasher: h, now: now, dummy: dummy}, nil
}

// ValidLogin проверяет правило «3–32 символа [A-Za-z0-9_.-]».
func ValidLogin(login string) bool {
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

// NormalizeLogin возвращает логин для сравнения без учёта регистра.
func NormalizeLogin(login string) string { return strings.ToLower(login) }

func validPassword(pw string) bool { return len(pw) >= minPassword && len(pw) <= maxPassword }

// Register создаёт пользователя. Слишком длинный пароль отвергается до вычисления хеша.
func (s *Service) Register(ctx context.Context, login, pw string) (store.UserRef, error) {
	if !ValidLogin(login) {
		return store.UserRef{}, ErrInvalidLogin
	}
	if !validPassword(pw) {
		return store.UserRef{}, ErrInvalidPassword
	}
	hash, err := s.hasher.Hash(ctx, pw)
	if err != nil {
		return store.UserRef{}, err
	}
	u, err := s.store.CreateUser(ctx, login, NormalizeLogin(login), hash)
	if err != nil {
		return store.UserRef{}, err
	}
	return store.UserRef{ID: u.ID, Login: u.Login}, nil
}

// Login проверяет пароль. Неверный пароль, неизвестный логин и логин вне правил дают одинаковый ответ
// и проверку хеша; пароль длиннее предела сразу отвергается, чтобы не тратить ресурсы.
func (s *Service) Login(ctx context.Context, login, pw string) (Session, error) {
	if len(pw) > maxPassword {
		return Session{}, ErrInvalidCredentials
	}
	user := store.User{Hash: s.dummy}
	known := false
	if ValidLogin(login) {
		u, err := s.store.UserByLogin(ctx, NormalizeLogin(login))
		switch {
		case err == nil:
			user, known = u, true
		case !errors.Is(err, store.ErrNotFound):
			return Session{}, err
		}
	}
	err := s.hasher.Verify(ctx, pw, user.Hash)
	if errors.Is(err, password.ErrMismatch) || (err == nil && !known) {
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, err
	}

	now := s.now()
	if err := s.store.DeleteExpiredSessions(ctx, user.ID, now); err != nil {
		return Session{}, err
	}
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return Session{}, fmt.Errorf("accounts: источник случайности: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := now.Add(sessionTTL)
	if err := s.store.CreateSession(ctx, hashToken(token), user.ID, expires); err != nil {
		return Session{}, err
	}
	return Session{Token: token, ExpiresAt: expires, User: store.UserRef{ID: user.ID, Login: user.Login}}, nil
}

// hashToken: у сессии 256 бит энтропии, поэтому быстрого хеша достаточно; похищенная база не даёт
// рабочих сессий.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Authenticate возвращает владельца действующей сессии.
func (s *Service) Authenticate(ctx context.Context, token string) (store.UserRef, error) {
	if token == "" {
		return store.UserRef{}, ErrUnauthenticated
	}
	u, err := s.store.SessionUser(ctx, hashToken(token), s.now())
	if errors.Is(err, store.ErrNotFound) {
		return store.UserRef{}, ErrUnauthenticated
	}
	return u, err
}

// Logout завершает сессию. Повторный выход даёт ошибку авторизации.
func (s *Service) Logout(ctx context.Context, token string) error {
	err := s.store.DeleteSession(ctx, hashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return ErrUnauthenticated
	}
	return err
}
