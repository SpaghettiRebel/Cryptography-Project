package main

import (
	"encoding/json"
	"os"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"cryptochat/client/internal/ciphers"
)

// smokeReport — результат пробного запуска окна без оператора.
type smokeReport struct {
	Frontend   string            `json:"frontend"`
	Algorithms []AlgorithmOption `json:"algorithms"`
	DevCipher  bool              `json:"devcipher"`
}

// smoke в режиме CRYPTOCHAT_SMOKE=<путь> записывает отчёт и закрывает приложение; без переменной
// ничего не делает.
func (a *App) smoke() error {
	path := os.Getenv("CRYPTOCHAT_SMOKE")
	if path == "" {
		return nil
	}
	raw, err := json.Marshal(smokeReport{
		Frontend:   "ready",
		Algorithms: optionsView(ciphers.Options()).Algorithms,
		DevCipher:  ciphers.DevBuild,
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	ctx := a.context()
	if ctx == nil {
		return nil
	}
	go func() {
		defer func() { _ = recover() }()
		time.Sleep(300 * time.Millisecond)
		runtime.Quit(ctx)
	}()
	return nil
}
