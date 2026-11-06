package main

import "context"

// readyAll объединяет проверки зависимостей: отказ любой из них означает, что сервер не готов.
func readyAll(checks ...func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		for _, check := range checks {
			if err := check(ctx); err != nil {
				return err
			}
		}
		return nil
	}
}
