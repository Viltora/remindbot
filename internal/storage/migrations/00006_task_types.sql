-- +goose Up
-- Which YouGile columns the bot takes tasks from, and of what type. The types
-- and what the bot does with each are defined in code (storage.TaskType); this
-- table only matches columns to them, so a renamed or new column needs no release.
CREATE TABLE task_type_columns (
    column_id  TEXT PRIMARY KEY, -- YouGile column ID
    title      TEXT        NOT NULL, -- column title in YouGile, for people
    task_type  TEXT        NOT NULL
               CHECK (task_type IN ('post', 'reels', 'stories', 'telegram')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Project «ИНСАЙТ», board «ПУБЛИКАЦИИ ВСЕГО».
INSERT INTO task_type_columns (column_id, title, task_type) VALUES
    ('88b38941-a424-4060-9124-578955eb0039', 'ПОСТЫ', 'post'),
    ('0f5a2559-1903-40b3-adec-242f4045eb96', 'РИЛСЫ', 'reels'),
    ('43ed727f-94c5-4be7-ba81-0350b0dee1c9', 'СТОРИС', 'stories'),
    ('0e782373-8886-4429-bea8-cf99d9a4002f', 'ТЕЛЕГРАМ', 'telegram');

-- +goose Down
DROP TABLE task_type_columns;
