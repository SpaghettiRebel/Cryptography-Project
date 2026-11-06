package rpc

import (
	"context"
	"errors"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"cryptochat/server/internal/accounts"
	"cryptochat/server/internal/chats"
	"cryptochat/server/internal/delivery"
	"cryptochat/server/internal/keyexchange"
	"cryptochat/server/internal/store"
)

// Domain — домен причин в ErrorInfo.
const Domain = "cryptochat"

// Причины ошибок API.
const (
	ReasonLoginTaken         = "LOGIN_TAKEN"
	ReasonInvalidLogin       = "INVALID_LOGIN"
	ReasonInvalidPassword    = "INVALID_PASSWORD"
	ReasonInvalidCredentials = "INVALID_CREDENTIALS"
	ReasonUnauthenticated    = "UNAUTHENTICATED"
	ReasonUserNotFound       = "USER_NOT_FOUND"
	ReasonSelfRequest        = "SELF_REQUEST"
	ReasonAlreadyContact     = "ALREADY_CONTACT"
	ReasonDuplicateRequest   = "DUPLICATE_REQUEST"
	ReasonRequestNotFound    = "REQUEST_NOT_FOUND"
	ReasonContactNotFound    = "CONTACT_NOT_FOUND"
	ReasonStoreUnavailable   = "STORE_UNAVAILABLE"
	// Чаты и распределение ключа.
	ReasonNotContact            = "NOT_CONTACT"
	ReasonSelfChat              = "SELF_CHAT"
	ReasonInvalidChatParams     = "INVALID_CHAT_PARAMS"
	ReasonInvalidChatID         = "INVALID_CHAT_ID"
	ReasonChatNotFound          = "CHAT_NOT_FOUND"
	ReasonChatClosed            = "CHAT_CLOSED"
	ReasonNotConnected          = "NOT_CONNECTED"
	ReasonInvalidPublicValue    = "INVALID_PUBLIC_VALUE"
	ReasonValueAlreadyPublished = "VALUE_ALREADY_PUBLISHED"
	// Доставка.
	ReasonInvalidFragment      = "INVALID_FRAGMENT"
	ReasonFragmentTooLarge     = "FRAGMENT_TOO_LARGE"
	ReasonChatNotReady         = "CHAT_NOT_READY"
	ReasonQueueFull            = "QUEUE_FULL"
	ReasonBrokerUnavailable    = "BROKER_UNAVAILABLE"
	ReasonStreamReplaced       = "STREAM_REPLACED"
	ReasonAckTimeout           = "ACK_TIMEOUT"
	ReasonInvalidStreamRequest = "INVALID_STREAM_REQUEST"
	ReasonServerStopping       = "SERVER_STOPPING"
)

// fieldKey — ключ имени неверного параметра в ErrorInfo.Metadata.
const fieldKey = "field"

type mapping struct {
	target  error
	code    codes.Code
	reason  string
	message string
}

var errorMappings = []mapping{
	{store.ErrLoginTaken, codes.AlreadyExists, ReasonLoginTaken, "логин занят"},
	{accounts.ErrInvalidLogin, codes.InvalidArgument, ReasonInvalidLogin, "недопустимый логин"},
	{accounts.ErrInvalidPassword, codes.InvalidArgument, ReasonInvalidPassword, "недопустимый пароль"},
	{accounts.ErrInvalidCredentials, codes.Unauthenticated, ReasonInvalidCredentials, "неверный логин или пароль"},
	{accounts.ErrUnauthenticated, codes.Unauthenticated, ReasonUnauthenticated, "нужна действующая сессия"},
	{store.ErrNotFound, codes.NotFound, ReasonUserNotFound, "пользователь не найден"},
	{store.ErrSelfRequest, codes.InvalidArgument, ReasonSelfRequest, "нельзя отправить запрос себе"},
	{store.ErrAlreadyContact, codes.AlreadyExists, ReasonAlreadyContact, "пользователь уже в контактах"},
	{store.ErrDuplicateRequest, codes.AlreadyExists, ReasonDuplicateRequest, "запрос уже отправлен"},
	{store.ErrRequestNotFound, codes.NotFound, ReasonRequestNotFound, "запрос не найден"},
	{store.ErrContactNotFound, codes.NotFound, ReasonContactNotFound, "контакт не найден"},
	{store.ErrUnavailable, codes.Unavailable, ReasonStoreUnavailable, "хранилище недоступно"},
	{store.ErrNotContact, codes.FailedPrecondition, ReasonNotContact, "собеседник не найден или не контакт"},
	{store.ErrSelfChat, codes.InvalidArgument, ReasonSelfChat, "нельзя создать чат с самим собой"},
	{chats.ErrInvalidChatID, codes.InvalidArgument, ReasonInvalidChatID, "идентификатор чата должен занимать 16 байт"},
	{store.ErrChatNotFound, codes.NotFound, ReasonChatNotFound, "чат не найден"},
	{store.ErrChatClosed, codes.FailedPrecondition, ReasonChatClosed, "чат закрыт"},
	{store.ErrNotConnected, codes.FailedPrecondition, ReasonNotConnected, "участник не подключён к чату"},
	{keyexchange.ErrInvalidPublicValue, codes.InvalidArgument, ReasonInvalidPublicValue, "открытое значение должно занимать 256 байт"},
	{store.ErrValueConflict, codes.AlreadyExists, ReasonValueAlreadyPublished, "опубликовано другое значение"},
	{delivery.ErrInvalidFragment, codes.InvalidArgument, ReasonInvalidFragment, "пустой фрагмент"},
	{delivery.ErrFragmentTooLarge, codes.InvalidArgument, ReasonFragmentTooLarge, "фрагмент больше допустимого"},
	{delivery.ErrChatNotReady, codes.FailedPrecondition, ReasonChatNotReady, "чат не готов к обмену"},
	{delivery.ErrQueueFull, codes.ResourceExhausted, ReasonQueueFull, "очередь получателя заполнена"},
	{delivery.ErrBrokerUnavailable, codes.Unavailable, ReasonBrokerUnavailable, "брокер недоступен"},
	{delivery.ErrStreamReplaced, codes.Aborted, ReasonStreamReplaced, "поток заменён новым"},
	{delivery.ErrAckTimeout, codes.DeadlineExceeded, ReasonAckTimeout, "нет подтверждения фрагмента"},
	{delivery.ErrInvalidStreamRequest, codes.InvalidArgument, ReasonInvalidStreamRequest, "неверная последовательность сообщений"},
	{delivery.ErrStopping, codes.Unavailable, ReasonServerStopping, "сервер останавливается"},
}

// toStatus превращает ошибку сервиса в ответ. Текст исходной ошибки наружу не передаётся.
func toStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "запрос отменён")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "истекло время запроса")
	}
	var param chats.ParamError
	if errors.As(err, &param) {
		return withDetails(codes.InvalidArgument, "неверный параметр чата: "+param.Field,
			&errdetails.ErrorInfo{Reason: ReasonInvalidChatParams, Domain: Domain, Metadata: map[string]string{fieldKey: param.Field}})
	}
	for _, m := range errorMappings {
		if errors.Is(err, m.target) {
			return withReason(m.code, m.reason, m.message)
		}
	}
	return status.Error(codes.Internal, "внутренняя ошибка")
}

func withReason(code codes.Code, reason, message string) error {
	return withDetails(code, message, &errdetails.ErrorInfo{Reason: reason, Domain: Domain})
}

func withDetails(code codes.Code, message string, info *errdetails.ErrorInfo) error {
	st := status.New(code, message)
	detailed, err := st.WithDetails(info)
	if err != nil {
		return st.Err()
	}
	return detailed.Err()
}

// FieldOf возвращает имя неверного параметра из ErrorInfo или пустую строку.
func FieldOf(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return ""
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.Domain == Domain {
			return info.GetMetadata()[fieldKey]
		}
	}
	return ""
}

// ReasonOf возвращает причину из ErrorInfo или пустую строку.
func ReasonOf(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return ""
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.Domain == Domain {
			return info.Reason
		}
	}
	return ""
}
