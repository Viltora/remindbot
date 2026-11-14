-- +goose Up
CREATE TABLE users (
    tg_id      BIGINT PRIMARY KEY,
    username   TEXT,
    first_name TEXT        NOT NULL DEFAULT '',
    role       TEXT        NOT NULL DEFAULT 'member'
               CHECK (role IN ('superadmin', 'admin', 'member')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE users;
