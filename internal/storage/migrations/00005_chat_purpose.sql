-- +goose Up
-- What a group chat is for: the content team or sales. NULL: not assigned yet.
ALTER TABLE chats ADD COLUMN purpose TEXT CHECK (purpose IN ('content', 'sales'));

-- +goose Down
ALTER TABLE chats DROP COLUMN purpose;
