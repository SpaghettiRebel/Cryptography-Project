package main

import (
	"context"
	"log"
	"os"
	"runtime/debug"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"cryptochat/client/internal/apperr"
	"cryptochat/client/internal/service"
)

// App — методы Go, доступные интерфейсу. Каждый экспортируемый метод начинается с defer a.guard(&err).
type App struct {
	ctxMu sync.Mutex
	ctx   context.Context // контекст окна; startup записывает его из другой горутины, чем вызовы методов
	svc   *service.Service
	log   *log.Logger
}

// NewApp создаёт фасад над сервисом.
func NewApp(svc *service.Service) *App {
	return &App{svc: svc, log: log.New(os.Stderr, "cryptochat: ", log.LstdFlags)}
}

// context возвращает контекст окна; до startup — nil.
func (a *App) context() context.Context {
	a.ctxMu.Lock()
	defer a.ctxMu.Unlock()
	return a.ctx
}

func (a *App) startup(ctx context.Context) {
	a.ctxMu.Lock()
	a.ctx = ctx
	a.ctxMu.Unlock()
	a.svc.Subscribe(service.EmitterFunc(func(event string, data any) {
		runtime.EventsEmit(ctx, event, eventView(data))
	}))
}

// shutdown закрывает сервис. Паника при закрытии не должна мешать завершению приложения: она
// записывается в журнал без значения.
func (a *App) shutdown(context.Context) {
	defer func() {
		if r := recover(); r != nil {
			a.log.Printf("внутренняя ошибка при завершении\n%s", debug.Stack())
		}
	}()
	a.svc.Close()
}

// FrontendReady вызывается интерфейсом после первой отрисовки.
func (a *App) FrontendReady() (err error) {
	defer a.guard(&err)
	return a.smoke()
}

func (a *App) GetSettings() (s Settings, err error) {
	defer a.guard(&err)
	v, err := a.svc.GetSettings()
	return settingsView(v), err
}

func (a *App) SaveSettings(in Settings) (s Settings, err error) {
	defer a.guard(&err)
	v, err := a.svc.SaveSettings(settingsModel(in))
	return settingsView(v), err
}

// ChooseCertificate открывает стандартный диалог выбора файла; отмена — пустая строка.
func (a *App) ChooseCertificate() (path string, err error) {
	defer a.guard(&err)
	ctx := a.context()
	if ctx == nil {
		// Без контекста окна runtime Wails завершает процесс, а не паникует.
		return "", apperr.New(apperr.Internal)
	}
	return runtime.OpenFileDialog(ctx, runtime.OpenDialogOptions{
		Title:   "Сертификат сервера",
		Filters: []runtime.FileFilter{{DisplayName: "Сертификат (*.crt, *.pem)", Pattern: "*.crt;*.pem"}},
	})
}

func (a *App) CheckServer() (err error) {
	defer a.guard(&err)
	return a.svc.CheckServer()
}

func (a *App) Register(login, password string) (err error) {
	defer a.guard(&err)
	return a.svc.Register(login, password)
}

func (a *App) Login(login, password string) (s Snapshot, err error) {
	defer a.guard(&err)
	v, err := a.svc.Login(login, password)
	return snapshotView(v), err
}

func (a *App) Logout() (err error) {
	defer a.guard(&err)
	return a.svc.Logout()
}

func (a *App) GetSnapshot() (s Snapshot, err error) {
	defer a.guard(&err)
	return snapshotView(a.svc.Snapshot()), nil
}

func (a *App) SendContactRequest(login string) (r ContactResult, err error) {
	defer a.guard(&err)
	became, err := a.svc.SendContactRequest(login)
	return ContactResult{BecameContact: became}, err
}

func (a *App) AcceptContact(login string) (err error) {
	defer a.guard(&err)
	return a.svc.AcceptContact(login)
}

func (a *App) RejectContact(login string) (err error) {
	defer a.guard(&err)
	return a.svc.RejectContact(login)
}

func (a *App) RemoveContact(login string) (err error) {
	defer a.guard(&err)
	return a.svc.RemoveContact(login)
}

func (a *App) ChatOptions() (o ChatOptions, err error) {
	defer a.guard(&err)
	return optionsView(a.svc.ChatOptions()), nil
}

func (a *App) CreateChat(req CreateChatRequest) (c ChatView, err error) {
	defer a.guard(&err)
	v, err := a.svc.CreateChat(service.CreateChatRequest(req))
	return chatView(v), err
}

func (a *App) ConnectChat(chatID string) (c ChatView, err error) {
	defer a.guard(&err)
	v, err := a.svc.ConnectChat(chatID)
	return chatView(v), err
}

func (a *App) DisconnectChat(chatID string) (c ChatView, err error) {
	defer a.guard(&err)
	v, err := a.svc.DisconnectChat(chatID)
	return chatView(v), err
}

func (a *App) CloseChat(chatID string) (err error) {
	defer a.guard(&err)
	return a.svc.CloseChat(chatID)
}
