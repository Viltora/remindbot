-- +goose Up
-- The content chat is split in two: 'material' gets the requests for material
-- for approval, 'publish' the publishing reminders and the content plan.
-- 'content' stays valid and gets both, so a chat assigned before keeps working.
ALTER TABLE chats DROP CONSTRAINT chats_purpose_check;
ALTER TABLE chats ADD CONSTRAINT chats_purpose_check
    CHECK (purpose IN ('content', 'sales', 'material', 'publish'));

-- +goose Down
UPDATE chats SET purpose = 'content' WHERE purpose IN ('material', 'publish');
ALTER TABLE chats DROP CONSTRAINT chats_purpose_check;
ALTER TABLE chats ADD CONSTRAINT chats_purpose_check CHECK (purpose IN ('content', 'sales'));
