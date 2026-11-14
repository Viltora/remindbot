-- +goose Up
-- When the user last started the bot in private. Only such users can get
-- messages from the bot: Telegram does not let bots write first.
ALTER TABLE users ADD COLUMN started_at TIMESTAMPTZ;

-- Group chats the bot has been added to.
CREATE TABLE chats (
    chat_id    BIGINT PRIMARY KEY,
    title      TEXT        NOT NULL DEFAULT '',
    active     BOOLEAN     NOT NULL DEFAULT true, -- the bot is in the chat now
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- People the bot has seen in group chats. The Bot API cannot list members,
-- so this only has those who wrote, pressed a button, joined or left.
CREATE TABLE chat_members (
    chat_id    BIGINT      NOT NULL REFERENCES chats (chat_id) ON DELETE CASCADE,
    tg_id      BIGINT      NOT NULL REFERENCES users (tg_id) ON DELETE CASCADE,
    present    BOOLEAN     NOT NULL DEFAULT true,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id, tg_id)
);

-- +goose Down
DROP TABLE chat_members;
DROP TABLE chats;
ALTER TABLE users DROP COLUMN started_at;
