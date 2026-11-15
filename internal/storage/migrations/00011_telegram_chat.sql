-- +goose Up
-- The "telegram" chat: where the day's message about the Telegram posts goes.
ALTER TABLE chats DROP CONSTRAINT chats_purpose_check;
ALTER TABLE chats ADD CONSTRAINT chats_purpose_check
    CHECK (purpose IN ('content', 'sales', 'material', 'publish', 'telegram'));

-- +goose Down
UPDATE chats SET purpose = NULL WHERE purpose = 'telegram';
ALTER TABLE chats DROP CONSTRAINT chats_purpose_check;
ALTER TABLE chats ADD CONSTRAINT chats_purpose_check
    CHECK (purpose IN ('content', 'sales', 'material', 'publish'));
