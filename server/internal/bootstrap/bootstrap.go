// Пакет bootstrap создаёт секреты развёртывания и настраивает TLS канала.
package bootstrap

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	DBPasswordFile = "db_password"
	ServerCertFile = "server.crt"
	ServerKeyFile  = "server.key"
	// BrokerPasswordFile читает сервер, BrokerConfigFile — брокер.
	BrokerPasswordFile = "rabbitmq_password"
	BrokerConfigFile   = "rabbitmq.conf"
	// BrokerUser — пользователь брокера, общий для сервера и файла настроек.
	BrokerUser = "cryptochat"
)

// Init создаёт отсутствующие секреты в secretsDir; существующие не меняет. Открытый сертификат всегда
// копируется в certsDir, поэтому после пересоздания тома у клиента не остаётся устаревшего файла.
// owner >= 0 назначает владельца ключа, чтобы его читал сервер без прав root.
func Init(secretsDir, certsDir string, owner int) error {
	if err := os.MkdirAll(secretsDir, 0o755); err != nil {
		return fmt.Errorf("bootstrap: папка секретов: %w", err)
	}
	if err := os.MkdirAll(certsDir, 0o755); err != nil {
		return fmt.Errorf("bootstrap: папка сертификатов: %w", err)
	}
	if err := ensurePassword(filepath.Join(secretsDir, DBPasswordFile)); err != nil {
		return err
	}
	if err := ensureBrokerSecrets(secretsDir); err != nil {
		return err
	}
	certPath := filepath.Join(secretsDir, ServerCertFile)
	keyPath := filepath.Join(secretsDir, ServerKeyFile)
	if err := ensureCertificate(certPath, keyPath); err != nil {
		return err
	}
	if owner >= 0 {
		if err := os.Chown(keyPath, owner, owner); err != nil {
			return fmt.Errorf("bootstrap: владелец ключа: %w", err)
		}
	}
	data, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("bootstrap: чтение сертификата: %w", err)
	}
	if err := os.WriteFile(filepath.Join(certsDir, ServerCertFile), data, 0o644); err != nil {
		return fmt.Errorf("bootstrap: копирование сертификата: %w", err)
	}
	return nil
}

func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	}
	return false, err
}

func ensurePassword(path string) error {
	ok, err := exists(path)
	if err != nil {
		return fmt.Errorf("bootstrap: файл пароля: %w", err)
	}
	if ok {
		return nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("bootstrap: источник случайности: %w", err)
	}
	// 0644: файл читают и сервер, и Postgres под разными пользователями; том подключён только к ним.
	if err := os.WriteFile(path, []byte(base64.RawURLEncoding.EncodeToString(raw)), 0o644); err != nil {
		return fmt.Errorf("bootstrap: запись пароля: %w", err)
	}
	return nil
}

// ensureBrokerSecrets создаёт пароль брокера и файл настроек. Переменной с файлом пароля образ брокера не
// поддерживает, поэтому пароль попадает к нему через файл настроек, который всегда собирается из файла
// пароля и не может с ним разойтись.
func ensureBrokerSecrets(secretsDir string) error {
	passwordPath := filepath.Join(secretsDir, BrokerPasswordFile)
	if err := ensurePassword(passwordPath); err != nil {
		return err
	}
	password, err := os.ReadFile(passwordPath)
	if err != nil {
		return errors.New("bootstrap: чтение пароля брокера")
	}
	config := fmt.Sprintf("default_user = %s\ndefault_pass = %s\nlog.console = true\n", BrokerUser, password)
	// 0644: как и пароль базы, файл читает другой пользователь контейнера.
	if err := os.WriteFile(filepath.Join(secretsDir, BrokerConfigFile), []byte(config), 0o644); err != nil {
		return errors.New("bootstrap: запись настроек брокера")
	}
	return nil
}

func ensureCertificate(certPath, keyPath string) error {
	haveCert, err := exists(certPath)
	if err != nil {
		return fmt.Errorf("bootstrap: файл сертификата: %w", err)
	}
	haveKey, err := exists(keyPath)
	if err != nil {
		return fmt.Errorf("bootstrap: файл ключа: %w", err)
	}
	if haveCert && haveKey {
		return nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("bootstrap: ключ: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("bootstrap: источник случайности: %w", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "cryptochat server"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost", "server"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("bootstrap: сертификат: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("bootstrap: ключ: %w", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return fmt.Errorf("bootstrap: запись ключа: %w", err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return fmt.Errorf("bootstrap: запись сертификата: %w", err)
	}
	return nil
}

// ServerTLS загружает сертификат; минимальная версия — TLS 1.3.
func ServerTLS(certPath, keyPath string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: загрузка сертификата сервера: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}, nil
}

// ClientTLS доверяет только указанному файлу сертификата.
func ClientTLS(caPath, serverName string) (*tls.Config, error) {
	data, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: чтение сертификата: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, errors.New("bootstrap: файл не содержит сертификата")
	}
	return &tls.Config{RootCAs: pool, ServerName: serverName, MinVersion: tls.VersionTLS13}, nil
}
