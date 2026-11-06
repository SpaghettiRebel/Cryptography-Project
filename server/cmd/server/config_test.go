package main

import (
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := loadConfig(envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8443" || c.DBHost != "postgres" || c.DBName != "cryptochat" || c.DBUser != "cryptochat" ||
		c.DBPasswordFile != "/secrets/db_password" || c.TLSCert != "/secrets/server.crt" || c.TLSKey != "/secrets/server.key" ||
		c.LogLevel != slog.LevelInfo {
		t.Fatalf("%+v", c)
	}
}

func TestOverrides(t *testing.T) {
	c, err := loadConfig(envMap(map[string]string{
		"CHAT_LISTEN": ":1", "CHAT_DB_HOST": "h", "CHAT_DB_NAME": "n", "CHAT_DB_USER": "u",
		"CHAT_DB_PASSWORD_FILE": "/p", "CHAT_TLS_CERT": "/c", "CHAT_TLS_KEY": "/k", "CHAT_LOG_LEVEL": "debug",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":1" || c.DBHost != "h" || c.DBName != "n" || c.DBUser != "u" || c.DBPasswordFile != "/p" ||
		c.TLSCert != "/c" || c.TLSKey != "/k" || c.LogLevel != slog.LevelDebug {
		t.Fatalf("%+v", c)
	}
}

func TestBadValues(t *testing.T) {
	if _, err := loadConfig(envMap(map[string]string{"CHAT_LOG_LEVEL": "громко"})); err == nil {
		t.Error("уровень журнала")
	}
	if _, err := loadConfig(envMap(map[string]string{"CHAT_SECRETS_OWNER": "x"})); err == nil {
		t.Error("владелец")
	}
}

func TestDatabaseURL(t *testing.T) {
	c, _ := loadConfig(envMap(nil))
	dsn, err := c.databaseURL(func(string) ([]byte, error) { return []byte("па/роль:secret@\n"), nil })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dsn, "postgres://cryptochat:") || !strings.HasSuffix(dsn, "@postgres/cryptochat?sslmode=disable") {
		t.Fatalf("%s", dsn)
	}
	if strings.Contains(dsn, "\n") {
		t.Fatal("перевод строки не обрезан")
	}
	if _, err := c.databaseURL(func(string) ([]byte, error) { return nil, &os.PathError{Op: "open", Path: "x", Err: os.ErrNotExist} }); err == nil ||
		!strings.Contains(err.Error(), "/secrets/db_password") {
		t.Fatalf("нет файла: %v", err)
	}
	if _, err := c.databaseURL(func(string) ([]byte, error) { return []byte(" \n"), nil }); err == nil {
		t.Fatal("пустой файл")
	}
}

func TestBrokerDefaults(t *testing.T) {
	c, err := loadConfig(envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.BrokerHost != "rabbitmq:5672" || c.BrokerUser != "cryptochat" || c.BrokerPassFile != "/secrets/rabbitmq_password" {
		t.Fatalf("%+v", c)
	}
	c, err = loadConfig(envMap(map[string]string{"CHAT_BROKER_HOST": "h:1", "CHAT_BROKER_USER": "u", "CHAT_BROKER_PASSWORD_FILE": "/p"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.BrokerHost != "h:1" || c.BrokerUser != "u" || c.BrokerPassFile != "/p" {
		t.Fatalf("%+v", c)
	}
}

func TestBrokerURL(t *testing.T) {
	c, _ := loadConfig(envMap(nil))
	const password = "па/роль:secret@?#%"
	raw, err := c.brokerURL(func(string) ([]byte, error) { return []byte(password + "\n"), nil })
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := u.User.Password()
	if u.Scheme != "amqp" || u.Host != "rabbitmq:5672" || u.User.Username() != "cryptochat" || got != password || u.Path != "/" {
		t.Fatalf("%s", raw)
	}
	_, err = c.brokerURL(func(string) ([]byte, error) { return nil, &os.PathError{Op: "open", Path: "x", Err: os.ErrNotExist} })
	if err == nil || !strings.Contains(err.Error(), "/secrets/rabbitmq_password") {
		t.Fatalf("нет файла: %v", err)
	}
	_, err = c.brokerURL(func(string) ([]byte, error) { return []byte(" \n"), nil })
	if err == nil {
		t.Fatal("пустой файл")
	}
	if strings.Contains(err.Error(), password) {
		t.Fatal("ошибка содержит пароль")
	}
}
