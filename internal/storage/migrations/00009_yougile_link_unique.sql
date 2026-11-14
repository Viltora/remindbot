-- +goose Up
-- A Telegram user is linked to at most one YouGile person.
CREATE UNIQUE INDEX yougile_users_tg_id ON yougile_users (tg_id) WHERE tg_id IS NOT NULL;

-- +goose Down
DROP INDEX yougile_users_tg_id;
