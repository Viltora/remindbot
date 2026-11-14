-- +goose Up
ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('superadmin', 'admin', 'sales', 'member'));

-- Daily report check for sales users: one row per person per working day.
CREATE TABLE sales_reports (
    report_date      DATE        NOT NULL,
    tg_id            BIGINT      NOT NULL REFERENCES users (tg_id) ON DELETE CASCADE,
    status           TEXT        NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'sent', 'not_sent', 'no_answer')),
    attempts         INT         NOT NULL DEFAULT 0, -- "sent it?" questions asked so far
    answered_attempt INT,                            -- attempt the last answer came on
    answered_at      TIMESTAMPTZ,
    closed           BOOLEAN     NOT NULL DEFAULT false, -- the day is summed up
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (report_date, tg_id)
);

-- +goose Down
DROP TABLE sales_reports;
UPDATE users SET role = 'member' WHERE role = 'sales';
ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('superadmin', 'admin', 'member'));
