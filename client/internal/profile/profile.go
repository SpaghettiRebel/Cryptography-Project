// Пакет profile — папка данных клиента, файл настроек и путь к базе профиля.
package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"cryptochat/client/internal/apperr"
)

const (
	settingsFile   = "settings.json"
	defaultAddress = "localhost:8443"
	maxRecent      = 5
	// defaultCert — копия сертификата, которую пишет deploy/compose.yaml при запуске из папки проекта.
	defaultCert = "deploy/certs/server.crt"
)

// Settings — настройки компьютера. Пароля, токена и ключей здесь нет.
type Settings struct {
	Version         int      `json:"version"`
	ServerAddress   string   `json:"server_address"`
	CertificatePath string   `json:"certificate_path"`
	RecentLogins    []string `json:"recent_logins"`
}

// Root возвращает корень данных: CRYPTOCHAT_HOME или папку настроек пользователя ОС.
func Root() (string, error) {
	if dir := os.Getenv("CRYPTOCHAT_HOME"); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "cryptochat"), nil
}

// Defaults возвращает настройки первого запуска. Если рядом есть сертификат развёртывания, путь к нему
// подставляется сразу.
func Defaults() Settings {
	s := Settings{Version: 1, ServerAddress: defaultAddress, RecentLogins: []string{}}
	if abs, err := filepath.Abs(filepath.FromSlash(defaultCert)); err == nil {
		if st, err := os.Stat(abs); err == nil && st.Mode().IsRegular() {
			s.CertificatePath = abs
		}
	}
	return s
}

// Load читает настройки. Нет файла — значения по умолчанию; файл повреждён — значения по умолчанию
// и ошибка SETTINGS_INVALID.
func Load(root string) (Settings, error) {
	raw, err := os.ReadFile(filepath.Join(root, settingsFile))
	if errors.Is(err, os.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return Defaults(), apperr.New(apperr.SettingsInvalid)
	}
	var s Settings
	if err := json.Unmarshal(raw, &s); err != nil || ValidateAddress(s.ServerAddress) != nil {
		return Defaults(), apperr.New(apperr.SettingsInvalid)
	}
	s.Version = 1
	if s.RecentLogins == nil {
		s.RecentLogins = []string{}
	}
	if len(s.RecentLogins) > maxRecent {
		s.RecentLogins = s.RecentLogins[:maxRecent]
	}
	return s, nil
}

// Save записывает настройки атомарно: временный файл в той же папке и переименование.
func Save(root string, s Settings) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	s.Version = 1
	if s.RecentLogins == nil {
		s.RecentLogins = []string{}
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(root, "settings-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(root, settingsFile))
}

// AddRecentLogin ставит логин первым в список подсказок без повторов.
func (s *Settings) AddRecentLogin(login string) {
	out := []string{login}
	for _, l := range s.RecentLogins {
		if !strings.EqualFold(l, login) && len(out) < maxRecent {
			out = append(out, l)
		}
	}
	s.RecentLogins = out
}

// ValidateAddress проверяет форму «хост:порт».
func ValidateAddress(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || strings.ContainsAny(host, " /\\") {
		return apperr.New(apperr.InvalidAddress)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return apperr.New(apperr.InvalidAddress)
	}
	return nil
}

// Host возвращает хост адреса для проверки имени в сертификате.
func Host(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// DBPath возвращает путь к базе профиля «адрес сервера + идентификатор пользователя».
func DBPath(root, address, userID string) string {
	sum := sha256.Sum256([]byte(address + "\n" + userID))
	return filepath.Join(root, "profiles", hex.EncodeToString(sum[:])[:16], "client.db")
}
