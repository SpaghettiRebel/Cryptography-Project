package service

// Имена событий для интерфейса.
const (
	EventSnapshot     = "snapshot"
	EventNotice       = "notice"
	EventSessionEnded = "session-ended"
)

// Виды уведомлений.
const (
	NoticeInfo    = "info"
	NoticeWarning = "warning"
	NoticeError   = "error"
)

// Причины завершения сессии.
const (
	EndedExpired   = "SESSION_EXPIRED"
	EndedLoggedOut = "LOGGED_OUT"
)

// Notice — разовое уведомление.
type Notice struct {
	Kind    string
	Code    string
	Message string
}

// SessionEnded — сессия завершена; интерфейс показывает окно входа с логином.
type SessionEnded struct {
	Code  string
	Login string
}

// Emitter получает события сервиса: Snapshot, Notice или SessionEnded.
type Emitter interface {
	Emit(event string, data any)
}

// EmitterFunc — функция как Emitter.
type EmitterFunc func(event string, data any)

// Emit вызывает функцию.
func (f EmitterFunc) Emit(event string, data any) { f(event, data) }
