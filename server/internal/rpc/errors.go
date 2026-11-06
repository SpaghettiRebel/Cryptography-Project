package rpc

import (
	"context"
	"errors"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"cryptochat/server/internal/accounts"
	"cryptochat/server/internal/store"
)

// Domain — домен причин в ErrorInfo.
const Domain = "cryptochat"

// Причины ошибок API (research R7).
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
)

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
	for _, m := range errorMappings {
		if errors.Is(err, m.target) {
			return withReason(m.code, m.reason, m.message)
		}
	}
	return status.Error(codes.Internal, "внутренняя ошибка")
}

func withReason(code codes.Code, reason, message string) error {
	st := status.New(code, message)
	detailed, err := st.WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: Domain})
	if err != nil {
		return st.Err()
	}
	return detailed.Err()
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
