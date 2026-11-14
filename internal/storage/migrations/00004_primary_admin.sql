-- +goose Up
-- The primary admin is the one the bot tags in chats and names to people
-- (e.g. "send the report to @nastya"). At most one.
ALTER TABLE users ADD COLUMN primary_admin BOOLEAN NOT NULL DEFAULT false;
CREATE UNIQUE INDEX users_one_primary_admin ON users ((true)) WHERE primary_admin;

-- +goose Down
DROP INDEX users_one_primary_admin;
ALTER TABLE users DROP COLUMN primary_admin;
