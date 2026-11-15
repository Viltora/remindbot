-- +goose Up
-- The "telegram" role: who posts to the Telegram channel.
ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('superadmin', 'admin', 'sales', 'telegram', 'member'));

-- +goose Down
UPDATE users SET role = 'member' WHERE role = 'telegram';
ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('superadmin', 'admin', 'sales', 'member'));
