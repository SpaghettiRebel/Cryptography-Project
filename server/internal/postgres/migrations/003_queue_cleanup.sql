ALTER TABLE chats ADD COLUMN queues_deleted_at timestamptz;
UPDATE chats SET queues_deleted_at = closed_at WHERE closed_at IS NOT NULL;
ALTER TABLE chats ADD CONSTRAINT chats_queues_after_close
    CHECK (queues_deleted_at IS NULL OR closed_at IS NOT NULL);
CREATE INDEX chats_queue_cleanup_idx ON chats (closed_at)
    WHERE closed_at IS NOT NULL AND queues_deleted_at IS NULL;
