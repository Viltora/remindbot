-- +goose Up
-- People from YouGile, to name them in messages. tg_id will link them to the
-- bot's users so the bot can tag them; not filled yet (docs/bot.md, 8.1).
CREATE TABLE yougile_users (
    id         TEXT PRIMARY KEY, -- YouGile user ID
    real_name  TEXT        NOT NULL DEFAULT '',
    email      TEXT        NOT NULL DEFAULT '',
    tg_id      BIGINT      REFERENCES users (tg_id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Publications copied from YouGile, and the state of the bot's reminders about
-- them. People's answers are kept only here, never written to YouGile.
CREATE TABLE tasks (
    id         TEXT PRIMARY KEY, -- YouGile task ID
    task_type  TEXT        NOT NULL
               CHECK (task_type IN ('post', 'reels', 'stories', 'telegram')),
    title      TEXT        NOT NULL,
    publish_at TIMESTAMPTZ NOT NULL,
    assignees  TEXT[]      NOT NULL DEFAULT '{}', -- YouGile user IDs
    active     BOOLEAN     NOT NULL DEFAULT true, -- still in a tracked column

    -- Material for approval (posts, reels). Steps start over when the task moves,
    -- unless the material has come.
    material_step        INT NOT NULL DEFAULT 0, -- 1 request, 2–4 reminders, 5 the admin was told
    material_step_at     TIMESTAMPTZ,
    material_received_at TIMESTAMPTZ,
    material_from        BIGINT, -- Telegram ID of whoever sent it

    -- Publishing: reminders before, then "published?" questions. Start over
    -- when the task moves, unless answered.
    reminders_sent   INT  NOT NULL DEFAULT 0, -- 1 an hour before, 2 half an hour before
    attempts         INT  NOT NULL DEFAULT 0, -- "published?" questions asked
    asked_at         TIMESTAMPTZ,
    status           TEXT NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'published', 'not_published', 'no_answer')),
    answered_attempt INT,
    answered_at      TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX tasks_active_publish_at ON tasks (publish_at) WHERE active;

-- Messages the bot sent about a task: a reply to one of them is matched to
-- the task.
CREATE TABLE task_messages (
    chat_id    BIGINT      NOT NULL,
    message_id BIGINT      NOT NULL,
    task_id    TEXT        NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    kind       TEXT        NOT NULL CHECK (kind IN ('material', 'publish')),
    sent_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id, message_id)
);

-- +goose Down
DROP TABLE task_messages;
DROP TABLE tasks;
DROP TABLE yougile_users;
