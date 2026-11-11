// Пакет apperr — ошибка клиента с постоянным кодом и сообщением для пользователя.
// Ошибка уходит в интерфейс строкой JSON {"code","message","field"}.
package apperr

import (
	"context"
	"encoding/json"
	"errors"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Коды ошибок клиента.
const (
	LoginTaken           = "LOGIN_TAKEN"
	InvalidLogin         = "INVALID_LOGIN"
	InvalidPassword      = "INVALID_PASSWORD"
	InvalidCredentials   = "INVALID_CREDENTIALS"
	SessionExpired       = "SESSION_EXPIRED"
	UserNotFound         = "USER_NOT_FOUND"
	SelfRequest          = "SELF_REQUEST"
	AlreadyContact       = "ALREADY_CONTACT"
	DuplicateRequest     = "DUPLICATE_REQUEST"
	RequestNotFound      = "REQUEST_NOT_FOUND"
	ContactNotFound      = "CONTACT_NOT_FOUND"
	NotContact           = "NOT_CONTACT"
	SelfChat             = "SELF_CHAT"
	InvalidChatParams    = "INVALID_CHAT_PARAMS"
	InvalidChatID        = "INVALID_CHAT_ID"
	ChatNotFound         = "CHAT_NOT_FOUND"
	ChatClosed           = "CHAT_CLOSED"
	NotConnected         = "NOT_CONNECTED"
	KeyLost              = "KEY_LOST"
	ServerBusy           = "SERVER_BUSY"
	ServerUnavailable    = "SERVER_UNAVAILABLE"
	Timeout              = "TIMEOUT"
	NotLoggedIn          = "NOT_LOGGED_IN"
	InvalidAddress       = "INVALID_ADDRESS"
	CertFile             = "CERT_FILE"
	CertInvalid          = "CERT_INVALID"
	ProfileBusy          = "PROFILE_BUSY"
	ProfileStorage       = "PROFILE_STORAGE"
	SettingsInvalid      = "SETTINGS_INVALID"
	AlgorithmUnavailable = "ALGORITHM_UNAVAILABLE"
	PeerValueInvalid     = "PEER_VALUE_INVALID"
	Internal             = "INTERNAL"

	// Причины сервера доставки.
	QueueFull      = "QUEUE_FULL"
	ChatNotReady   = "CHAT_NOT_READY"
	StreamReplaced = "STREAM_REPLACED"
	AckTimeout     = "ACK_TIMEOUT"

	// Локальные причины переписки.
	EmptyText        = "EMPTY_TEXT"
	TextTooLong      = "TEXT_TOO_LONG"
	FileTooLarge     = "FILE_TOO_LARGE"
	FileRead         = "FILE_READ"
	SourceChanged    = "SOURCE_CHANGED"
	SendBlocked      = "SEND_BLOCKED"
	MessageNotFound  = "MESSAGE_NOT_FOUND"
	NotCancellable   = "NOT_CANCELLABLE"
	NotRetryable     = "NOT_RETRYABLE"
	NotAFile         = "NOT_A_FILE"
	FileWrite        = "FILE_WRITE"
	ChatActive       = "CHAT_ACTIVE"
	MessageCorrupted = "MESSAGE_CORRUPTED"
	KeyMissing       = "KEY_MISSING"
	LocalWrite       = "LOCAL_WRITE"
)

// Причины недоступности отправки (поле field ошибки SEND_BLOCKED).
const (
	BlockKeyNotReady          = "KEY_NOT_READY"
	BlockNotConnected         = "NOT_CONNECTED"
	BlockReceivingElsewhere   = "RECEIVING_ELSEWHERE"
	BlockAlgorithmUnavailable = "ALGORITHM_UNAVAILABLE"
	BlockChatClosed           = "CHAT_CLOSED"
)

var messages = map[string]string{
	LoginTaken:           "Логин уже занят",
	InvalidLogin:         "Логин: 3–32 символа, латинские буквы, цифры, _, ., -",
	InvalidPassword:      "Пароль: от 8 до 128 байт",
	InvalidCredentials:   "Неверный логин или пароль",
	SessionExpired:       "Сессия истекла, войдите снова",
	UserNotFound:         "Пользователь не найден",
	SelfRequest:          "Нельзя добавить в контакты себя",
	AlreadyContact:       "Пользователь уже в контактах",
	DuplicateRequest:     "Запрос уже отправлен",
	RequestNotFound:      "Запрос уже отменён или обработан",
	ContactNotFound:      "Контакт уже удалён",
	NotContact:           "Пользователь больше не ваш контакт",
	SelfChat:             "Нельзя создать чат с собой",
	InvalidChatParams:    "Недопустимый параметр чата",
	InvalidChatID:        "Неверный идентификатор чата",
	ChatNotFound:         "Чат не найден или удалён на сервере",
	ChatClosed:           "Чат закрыт",
	NotConnected:         "Сначала подключитесь к чату",
	KeyLost:              "Ключ на этом устройстве получить нельзя: закройте чат и создайте новый",
	ServerBusy:           "Сервер временно не может выполнить запрос, повторите позже",
	ServerUnavailable:    "Нет связи с сервером",
	Timeout:              "Сервер не ответил вовремя",
	NotLoggedIn:          "Войдите в учётную запись",
	InvalidAddress:       "Адрес сервера: хост:порт",
	CertFile:             "Не удалось прочитать файл сертификата",
	CertInvalid:          "Сертификат не подходит к серверу (выдан для localhost, server, 127.0.0.1); выберите актуальный файл",
	ProfileBusy:          "Этот профиль уже открыт в другом окне приложения",
	ProfileStorage:       "Не удалось открыть данные профиля",
	SettingsInvalid:      "Настройки повреждены и сброшены",
	AlgorithmUnavailable: "Алгоритм не реализован в этой сборке",
	PeerValueInvalid:     "Собеседник прислал недопустимое значение; закройте чат",
	Internal:             "Внутренняя ошибка клиента",

	QueueFull:      "Очередь собеседника на сервере заполнена: передача продолжится, когда он примет данные",
	ChatNotReady:   "Ключ чата ещё не согласован на сервере",
	StreamReplaced: "Приём этого чата идёт на другом устройстве",
	AckTimeout:     "Приём прерван сервером, возобновляется",

	EmptyText:        "Введите текст сообщения",
	TextTooLong:      "Сообщение длиннее 64 КиБ: отправьте его файлом",
	FileTooLarge:     "Файл больше 256 МиБ",
	FileRead:         "Не удалось прочитать файл",
	SourceChanged:    "Файл изменён во время отправки: выберите его снова",
	SendBlocked:      "Отправка недоступна",
	MessageNotFound:  "Сообщение не найдено",
	NotCancellable:   "Операция уже завершена",
	NotRetryable:     "Повтор расшифрования недоступен",
	NotAFile:         "Это сообщение нельзя сохранить как файл",
	FileWrite:        "Не удалось сохранить файл",
	ChatActive:       "Сначала закройте чат",
	MessageCorrupted: "Сообщение повреждено и не может быть показано",
	KeyMissing:       "Ключ этого чата удалён: сообщение нельзя расшифровать",
	LocalWrite:       "Не удалось записать данные на устройство: операция повторится",
}

// blockMessages — тексты SEND_BLOCKED по причине.
var blockMessages = map[string]string{
	BlockKeyNotReady:          "Ключ ещё не согласован",
	BlockNotConnected:         "Подключитесь к чату",
	BlockReceivingElsewhere:   "Приём идёт на другом устройстве",
	BlockAlgorithmUnavailable: "Алгоритм не реализован в этой сборке",
	BlockChatClosed:           "Чат закрыт",
}

// Blocked — отправка недоступна по причине block; причина уходит в поле field.
func Blocked(block string) *Error {
	e := WithField(SendBlocked, block)
	if m, ok := blockMessages[block]; ok {
		e.Message = m
	}
	return e
}

// Error — ошибка клиента. Message не содержит паролей, токенов, ключей и идентификаторов чатов.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

// Error возвращает JSON, который разбирает интерфейс.
func (e *Error) Error() string {
	b, err := json.Marshal(e)
	if err != nil {
		return `{"code":"INTERNAL","message":"Внутренняя ошибка клиента"}`
	}
	return string(b)
}

// New создаёт ошибку с сообщением по коду.
func New(code string) *Error {
	msg, ok := messages[code]
	if !ok {
		code, msg = Internal, messages[Internal]
	}
	return &Error{Code: code, Message: msg}
}

// WithField создаёт ошибку, относящуюся к полю формы.
func WithField(code, field string) *Error {
	e := New(code)
	e.Field = field
	return e
}

// Profile — ошибка открытия данных профиля с путём к папке профиля.
func Profile(dir string) *Error {
	return &Error{Code: ProfileStorage, Message: messages[ProfileStorage] + ": " + dir}
}

// As возвращает ошибку клиента из цепочки.
func As(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// CodeOf возвращает код ошибки клиента; для чужой ошибки — INTERNAL, для nil — пустую строку.
func CodeOf(err error) string {
	if err == nil {
		return ""
	}
	if e, ok := As(err); ok {
		return e.Code
	}
	return Internal
}

// Is сообщает, что err — ошибка клиента с одним из кодов.
func Is(err error, codes ...string) bool {
	c := CodeOf(err)
	for _, want := range codes {
		if c == want {
			return true
		}
	}
	return false
}

// reasons — причины ErrorInfo домена cryptochat и их коды клиента.
var reasons = map[string]string{
	"LOGIN_TAKEN":             LoginTaken,
	"INVALID_LOGIN":           InvalidLogin,
	"INVALID_PASSWORD":        InvalidPassword,
	"INVALID_CREDENTIALS":     InvalidCredentials,
	"UNAUTHENTICATED":         SessionExpired,
	"USER_NOT_FOUND":          UserNotFound,
	"SELF_REQUEST":            SelfRequest,
	"ALREADY_CONTACT":         AlreadyContact,
	"DUPLICATE_REQUEST":       DuplicateRequest,
	"REQUEST_NOT_FOUND":       RequestNotFound,
	"CONTACT_NOT_FOUND":       ContactNotFound,
	"NOT_CONTACT":             NotContact,
	"SELF_CHAT":               SelfChat,
	"INVALID_CHAT_PARAMS":     InvalidChatParams,
	"INVALID_CHAT_ID":         InvalidChatID,
	"CHAT_NOT_FOUND":          ChatNotFound,
	"CHAT_CLOSED":             ChatClosed,
	"NOT_CONNECTED":           NotConnected,
	"VALUE_ALREADY_PUBLISHED": KeyLost,
	// Своё значение всегда 256 байт: отказ означает ошибку клиента.
	"INVALID_PUBLIC_VALUE": Internal,
	// Временная недоступность хранилища или брокера и остановка сервера не сбрасывают сессию.
	"STORE_UNAVAILABLE":  ServerBusy,
	"BROKER_UNAVAILABLE": ServerBusy,
	"SERVER_STOPPING":    ServerBusy,
	// Причины передачи фрагментов. Отказ в формате фрагмента или запроса потока означает ошибку клиента.
	"QUEUE_FULL":             QueueFull,
	"CHAT_NOT_READY":         ChatNotReady,
	"STREAM_REPLACED":        StreamReplaced,
	"ACK_TIMEOUT":            AckTimeout,
	"INVALID_FRAGMENT":       Internal,
	"FRAGMENT_TOO_LARGE":     Internal,
	"INVALID_STREAM_REQUEST": Internal,
}

var fields = map[string]string{InvalidLogin: "login", InvalidPassword: "password"}

// FromStatus переводит ошибку вызова gRPC в ошибку клиента. Текст ответа сервера не используется.
func FromStatus(err error) *Error {
	if err == nil {
		return nil
	}
	if e, ok := As(err); ok {
		return e
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return New(Timeout)
	}
	st, ok := status.FromError(err)
	if !ok {
		return New(Internal)
	}
	for _, d := range st.Details() {
		info, ok := d.(*errdetails.ErrorInfo)
		if !ok || info.GetDomain() != "cryptochat" {
			continue
		}
		code, known := reasons[info.GetReason()]
		if !known {
			break
		}
		e := New(code)
		if f := fields[code]; f != "" {
			e.Field = f
		}
		if code == InvalidChatParams {
			e.Field = info.GetMetadata()["field"]
		}
		return e
	}
	switch st.Code() {
	case codes.Unavailable:
		return New(ServerUnavailable)
	case codes.DeadlineExceeded:
		return New(Timeout)
	}
	return New(Internal)
}
