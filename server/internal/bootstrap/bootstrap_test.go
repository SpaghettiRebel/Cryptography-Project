package bootstrap

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestInitCreatesSecrets(t *testing.T) {
	secrets, certs := t.TempDir(), t.TempDir()
	if err := Init(secrets, certs, -1); err != nil {
		t.Fatal(err)
	}
	password, err := os.ReadFile(filepath.Join(secrets, DBPasswordFile))
	if err != nil || len(password) < 32 {
		t.Fatalf("пароль: %d байт, %v", len(password), err)
	}
	if runtime.GOOS != "windows" {
		for name, want := range map[string]os.FileMode{ServerKeyFile: 0o600, DBPasswordFile: 0o644} {
			info, err := os.Stat(filepath.Join(secrets, name))
			if err != nil || info.Mode().Perm() != want {
				t.Errorf("%s: %v, %v", name, info.Mode().Perm(), err)
			}
		}
	}
	block, _ := pem.Decode(mustRead(t, filepath.Join(secrets, ServerCertFile)))
	if block == nil {
		t.Fatal("сертификат не в PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.VerifyHostname("localhost"); err != nil {
		t.Error(err)
	}
	if err := cert.VerifyHostname("server"); err != nil {
		t.Error(err)
	}
	if err := cert.VerifyHostname("127.0.0.1"); err != nil {
		t.Error(err)
	}
	if d := time.Until(cert.NotAfter); d < 360*24*time.Hour || d > 370*24*time.Hour {
		t.Errorf("срок действия %v", d)
	}
	if !bytes.Equal(mustRead(t, filepath.Join(certs, ServerCertFile)), mustRead(t, filepath.Join(secrets, ServerCertFile))) {
		t.Error("открытый сертификат не скопирован")
	}
	if _, err := os.Stat(filepath.Join(certs, ServerKeyFile)); err == nil {
		t.Error("ключ не должен попадать в папку клиента")
	}
}

func TestInitKeepsSecretsAndRefreshesPublicCert(t *testing.T) {
	secrets, certs := t.TempDir(), t.TempDir()
	if err := Init(secrets, certs, -1); err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, name := range []string{DBPasswordFile, ServerCertFile, ServerKeyFile} {
		before[name] = mustRead(t, filepath.Join(secrets, name))
	}
	// Устаревший файл в папке клиента, как после down -v.
	if err := os.WriteFile(filepath.Join(certs, ServerCertFile), []byte("старый"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Init(secrets, certs, -1); err != nil {
		t.Fatal(err)
	}
	for name, want := range before {
		if !bytes.Equal(mustRead(t, filepath.Join(secrets, name)), want) {
			t.Errorf("%s изменён", name)
		}
	}
	if !bytes.Equal(mustRead(t, filepath.Join(certs, ServerCertFile)), before[ServerCertFile]) {
		t.Error("открытый сертификат не обновлён")
	}
}

func TestTLSHandshake(t *testing.T) {
	secrets, certs := t.TempDir(), t.TempDir()
	if err := Init(secrets, certs, -1); err != nil {
		t.Fatal(err)
	}
	serverCfg, err := ServerTLS(filepath.Join(secrets, ServerCertFile), filepath.Join(secrets, ServerKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if serverCfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("минимальная версия %x", serverCfg.MinVersion)
	}
	clientCfg, err := ClientTLS(filepath.Join(certs, ServerCertFile), "localhost")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.WriteString(c, "ok")
			}()
		}
	}()
	conn, err := tls.Dial("tcp", ln.Addr().String(), clientCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	got, _ := io.ReadAll(conn)
	if string(got) != "ok" {
		t.Fatalf("ответ %q", got)
	}

	// Без доверия к сертификату соединение не устанавливается.
	if _, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{ServerName: "localhost", MinVersion: tls.VersionTLS13}); err == nil {
		t.Error("соединение без доверия к сертификату установилось")
	}
	// TLS 1.2 отвергается.
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	old := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12}) //nolint:gosec // проверка отказа
	if err := old.Handshake(); err == nil {
		t.Error("TLS 1.2 принят")
	}
}

func TestClientTLSErrors(t *testing.T) {
	if _, err := ClientTLS(filepath.Join(t.TempDir(), "нет.crt"), "localhost"); err == nil {
		t.Error("нет файла")
	}
	bad := filepath.Join(t.TempDir(), "bad.crt")
	if err := os.WriteFile(bad, []byte("мусор"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ClientTLS(bad, "localhost"); err == nil {
		t.Error("мусор вместо сертификата")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
