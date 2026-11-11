package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"cryptochat/client/internal/ciphers"
)

// smokeReport — результат пробного запуска окна без оператора.
type smokeReport struct {
	Frontend   string            `json:"frontend"`
	Algorithms []AlgorithmOption `json:"algorithms"`
	DevCipher  bool              `json:"devcipher"`
	// Media — дошёл ли запрос страницы /media/... до обработчика картинок: handler или unreached.
	Media string `json:"media"`
}

// mediaCounter считает запросы к обработчику картинок: пробный запуск проверяет, что окно их передаёт.
type mediaCounter struct {
	h    http.Handler
	hits atomic.Int64
}

func (m *mediaCounter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.hits.Add(1)
	m.h.ServeHTTP(w, r)
}

// probeMedia просит страницу запросить /media/0 и ждёт обращения к обработчику до 3 с.
func (a *App) probeMedia(ctx context.Context) string {
	if a.media == nil || ctx == nil {
		return "unreached"
	}
	runtime.WindowExecJS(ctx, `fetch("/media/0").catch(function(){})`)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if a.media.hits.Load() > 0 {
			return "handler"
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "unreached"
}

// smoke в режиме CRYPTOCHAT_SMOKE=<путь> записывает отчёт и закрывает приложение; без переменной
// ничего не делает.
func (a *App) smoke() error {
	path := os.Getenv("CRYPTOCHAT_SMOKE")
	if path == "" {
		return nil
	}
	ctx := a.context()
	report := smokeReport{
		Frontend:   "ready",
		Algorithms: optionsView(ciphers.Options()).Algorithms,
		DevCipher:  ciphers.DevBuild,
	}
	write := func() error {
		raw, err := json.Marshal(report)
		if err != nil {
			return err
		}
		return os.WriteFile(path, raw, 0o600)
	}
	if ctx == nil {
		report.Media = "unreached"
		return write()
	}
	// Проверка обработчика идёт после ответа на FrontendReady: страница должна быть свободна.
	go func() {
		defer func() { _ = recover() }()
		report.Media = a.probeMedia(ctx)
		if err := write(); err != nil {
			a.log.Printf("пробный запуск: отчёт не записан")
		}
		time.Sleep(300 * time.Millisecond)
		runtime.Quit(ctx)
	}()
	return nil
}
