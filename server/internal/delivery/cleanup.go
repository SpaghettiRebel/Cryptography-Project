package delivery

import (
	"context"
	"time"
)

// RunCleanup удаляет очереди закрытых чатов, пока ctx не отменён. Обязательство фиксирует сам признак закрытия
// в той же транзакции, поэтому покрыты все пути закрытия, сбой брокера и перезапуск сервера.
func (s *Service) RunCleanup(ctx context.Context) {
	ticker := time.NewTicker(s.opt.CleanupInterval)
	defer ticker.Stop()
	for {
		s.cleanupPass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) cleanupPass(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if !s.deleteOrphans(ctx) {
		return
	}
	list, err := s.store.PendingQueueCleanup(ctx, CleanupBatch)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("удаление очередей отложено: хранилище не ответило")
		}
		return
	}
	for _, chat := range list {
		// Удаление идёт до отметки и идемпотентно: падение между ними приведёт только к повтору.
		for _, user := range []string{chat.UserLow, chat.UserHigh} {
			queue := QueueName(chat.ID, user)
			if !s.deleteQueue(ctx, queue) {
				return
			}
			s.forget(queue)
		}
		if err := s.store.MarkQueuesDeleted(ctx, chat.ID); err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("удаление очередей отложено: хранилище не ответило")
			}
			return
		}
	}
}

// deleteQueue сообщает об успехе. При сбое брокера проход прекращается: повторит следующий.
func (s *Service) deleteQueue(ctx context.Context, queue string) bool {
	deleteCtx, cancel := context.WithTimeout(ctx, PublishTimeout)
	defer cancel()
	if err := s.broker.DeleteQueue(deleteCtx, queue); err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("удаление очередей отложено: брокер не ответил")
		}
		return false
	}
	return true
}

func (s *Service) deleteOrphans(ctx context.Context) bool {
	s.mu.Lock()
	queues := make([]string, 0, len(s.orphans))
	for q := range s.orphans {
		queues = append(queues, q)
	}
	s.mu.Unlock()
	for _, q := range queues {
		if !s.deleteQueue(ctx, q) {
			return false
		}
		s.mu.Lock()
		delete(s.orphans, q)
		s.mu.Unlock()
	}
	return true
}
