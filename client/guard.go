package main

import (
	"runtime/debug"

	"cryptochat/client/internal/apperr"
)

// guard стоит первой строкой каждого привязанного метода: Wails v2.9.2 не перехватывает панику, и без
// guard она завершила бы приложение. Паника и любая ошибка не из apperr превращаются в INTERNAL;
// значение паники в журнал не пишется.
func (a *App) guard(err *error) {
	if r := recover(); r != nil {
		a.log.Printf("внутренняя ошибка в привязанном методе\n%s", debug.Stack())
		*err = apperr.New(apperr.Internal)
		return
	}
	if *err == nil {
		return
	}
	if e, ok := apperr.As(*err); ok {
		*err = e
		return
	}
	a.log.Printf("неожиданная ошибка: %T", *err)
	*err = apperr.New(apperr.Internal)
}
