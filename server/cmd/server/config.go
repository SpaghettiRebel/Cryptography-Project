package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
)

// config — параметры сервера. Секреты передаются только через файлы.
type config struct {
	Listen         string
	DBHost         string
	DBName         string
	DBUser         string
	DBPasswordFile string
	BrokerHost     string
	BrokerUser     string
	BrokerPassFile string
	TLSCert        string
	TLSKey         string
	LogLevel       slog.Level
	SecretsDir     string
	CertsDir       string
	SecretsOwner   int
	HealthAddr     string
}

func env(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadConfig(getenv func(string) string) (config, error) {
	c := config{
		Listen:         env(getenv, "CHAT_LISTEN", ":8443"),
		DBHost:         env(getenv, "CHAT_DB_HOST", "postgres"),
		DBName:         env(getenv, "CHAT_DB_NAME", "cryptochat"),
		DBUser:         env(getenv, "CHAT_DB_USER", "cryptochat"),
		DBPasswordFile: env(getenv, "CHAT_DB_PASSWORD_FILE", "/secrets/db_password"),
		BrokerHost:     env(getenv, "CHAT_BROKER_HOST", "rabbitmq:5672"),
		BrokerUser:     env(getenv, "CHAT_BROKER_USER", "cryptochat"),
		BrokerPassFile: env(getenv, "CHAT_BROKER_PASSWORD_FILE", "/secrets/rabbitmq_password"),
		TLSCert:        env(getenv, "CHAT_TLS_CERT", "/secrets/server.crt"),
		TLSKey:         env(getenv, "CHAT_TLS_KEY", "/secrets/server.key"),
		SecretsDir:     env(getenv, "CHAT_SECRETS_DIR", "/secrets"),
		CertsDir:       env(getenv, "CHAT_CERTS_OUT", "/certs"),
		HealthAddr:     env(getenv, "CHAT_HEALTH_ADDR", "localhost:8443"),
	}
	owner, err := strconv.Atoi(env(getenv, "CHAT_SECRETS_OWNER", "65532"))
	if err != nil {
		return config{}, errors.New("config: CHAT_SECRETS_OWNER должен быть числом")
	}
	c.SecretsOwner = owner
	if err := c.LogLevel.UnmarshalText([]byte(env(getenv, "CHAT_LOG_LEVEL", "info"))); err != nil {
		return config{}, errors.New("config: неизвестный CHAT_LOG_LEVEL")
	}
	return c, nil
}

// databaseURL читает пароль из файла. Ошибка не содержит его значения.
func (c config) databaseURL(readFile func(string) ([]byte, error)) (string, error) {
	raw, err := readFile(c.DBPasswordFile)
	if err != nil {
		return "", fmt.Errorf("config: файл пароля базы %s: %w", c.DBPasswordFile, errors.Unwrap(err))
	}
	password := strings.TrimSpace(string(raw))
	if password == "" {
		return "", fmt.Errorf("config: файл пароля базы %s пуст", c.DBPasswordFile)
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.DBUser, password),
		Host:     c.DBHost,
		Path:     "/" + c.DBName,
		RawQuery: "sslmode=disable",
	}
	return u.String(), nil
}

// brokerURL читает пароль брокера из файла. Ошибка не содержит его значения.
func (c config) brokerURL(readFile func(string) ([]byte, error)) (string, error) {
	raw, err := readFile(c.BrokerPassFile)
	if err != nil {
		return "", fmt.Errorf("config: файл пароля брокера %s: %w", c.BrokerPassFile, errors.Unwrap(err))
	}
	password := strings.TrimSpace(string(raw))
	if password == "" {
		return "", fmt.Errorf("config: файл пароля брокера %s пуст", c.BrokerPassFile)
	}
	u := url.URL{Scheme: "amqp", User: url.UserPassword(c.BrokerUser, password), Host: c.BrokerHost, Path: "/"}
	return u.String(), nil
}
