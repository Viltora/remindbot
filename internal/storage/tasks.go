package storage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type PublishStatus string

const (
	PublishPending  PublishStatus = "pending"       // no answer yet
	PublishDone     PublishStatus = "published"     // answered "yes"
	PublishNotDone  PublishStatus = "not_published" // answered "no"
	PublishNoAnswer PublishStatus = "no_answer"     // the questions ran out
)

// Task is a publication from YouGile and the state of the reminders about it.
// Zero times mean "not yet".
type Task struct {
	ID        string
	Type      TaskType
	Title     string
	PublishAt time.Time
	Assignees []string // YouGile user IDs

	MaterialStep       int // 1 request, 2–4 reminders, 5 the admin was told
	MaterialStepAt     time.Time
	MaterialReceivedAt time.Time

	RemindersSent   int // reminders sent before P; one since 14.11.2026, half an hour before
	Attempts        int // "published?" questions asked
	AskedAt         time.Time
	Status          PublishStatus
	AnsweredAttempt int
}

type YouGileUser struct {
	ID       string
	RealName string
	Email    string
}

// Kinds of messages the bot sends about a task.
const (
	TaskMsgMaterial = "material"
	TaskMsgPublish  = "publish"
)

const taskColumns = `
	id, task_type, title, publish_at, assignees,
	material_step, material_step_at, material_received_at,
	reminders_sent, attempts, asked_at, status, COALESCE(answered_attempt, 0)`

func scanTask(row pgx.CollectableRow) (Task, error) {
	var t Task
	var stepAt, receivedAt, askedAt *time.Time
	err := row.Scan(&t.ID, &t.Type, &t.Title, &t.PublishAt, &t.Assignees,
		&t.MaterialStep, &stepAt, &receivedAt,
		&t.RemindersSent, &t.Attempts, &askedAt, &t.Status, &t.AnsweredAttempt)
	t.MaterialStepAt, t.MaterialReceivedAt, t.AskedAt = orZero(stepAt), orZero(receivedAt), orZero(askedAt)
	return t, err
}

func orZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// SyncTasks makes the copy of the tasks published within [from, to) match
// tasks, read from YouGile just now. Tasks in the window that are gone (deleted,
// archived, moved to another column or time) stop being active. A task whose
// publish time changed starts its reminders over; only the received material
// and a "published" answer stay.
func (s *Store) SyncTasks(ctx context.Context, users []YouGileUser, tasks []Task, from, to time.Time) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for _, u := range users {
			batch.Queue(`
				INSERT INTO yougile_users (id, real_name, email) VALUES ($1, $2, $3)
				ON CONFLICT (id) DO UPDATE
				SET real_name = EXCLUDED.real_name, email = EXCLUDED.email, updated_at = now()
				WHERE (yougile_users.real_name, yougile_users.email)
				      IS DISTINCT FROM (EXCLUDED.real_name, EXCLUDED.email)`,
				u.ID, u.RealName, u.Email)
		}
		ids := make([]string, len(tasks))
		for i, t := range tasks {
			ids[i] = t.ID
			assignees := t.Assignees
			if assignees == nil {
				assignees = []string{}
			}
			// In SET, tasks.* are the values before the update.
			batch.Queue(`
				INSERT INTO tasks (id, task_type, title, publish_at, assignees) VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (id) DO UPDATE
				SET task_type = EXCLUDED.task_type,
				    title     = EXCLUDED.title,
				    assignees = EXCLUDED.assignees,
				    publish_at = EXCLUDED.publish_at,
				    active     = true,
				    material_step = CASE
				        WHEN tasks.publish_at <> EXCLUDED.publish_at AND tasks.material_received_at IS NULL
				        THEN 0 ELSE tasks.material_step END,
				    reminders_sent = CASE
				        WHEN tasks.publish_at <> EXCLUDED.publish_at AND tasks.status <> 'published'
				        THEN 0 ELSE tasks.reminders_sent END,
				    attempts = CASE
				        WHEN tasks.publish_at <> EXCLUDED.publish_at AND tasks.status <> 'published'
				        THEN 0 ELSE tasks.attempts END,
				    status = CASE
				        WHEN tasks.publish_at <> EXCLUDED.publish_at AND tasks.status <> 'published'
				        THEN 'pending' ELSE tasks.status END,
				    answered_attempt = CASE
				        WHEN tasks.publish_at <> EXCLUDED.publish_at AND tasks.status <> 'published'
				        THEN NULL ELSE tasks.answered_attempt END,
				    answered_at = CASE
				        WHEN tasks.publish_at <> EXCLUDED.publish_at AND tasks.status <> 'published'
				        THEN NULL ELSE tasks.answered_at END,
				    updated_at = now()
				WHERE (tasks.task_type, tasks.title, tasks.assignees, tasks.publish_at, tasks.active)
				      IS DISTINCT FROM (EXCLUDED.task_type, EXCLUDED.title, EXCLUDED.assignees, EXCLUDED.publish_at, true)`,
				t.ID, t.Type, t.Title, t.PublishAt, assignees)
		}
		batch.Queue(`
			UPDATE tasks SET active = false, updated_at = now()
			WHERE active AND publish_at >= $1 AND publish_at < $2 AND NOT (id = ANY($3))`,
			from, to, ids)
		return tx.SendBatch(ctx, batch).Close()
	})
}

// ActiveTasks returns the active tasks published within [from, to), earliest first.
func (s *Store) ActiveTasks(ctx context.Context, from, to time.Time) ([]Task, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+taskColumns+` FROM tasks
		WHERE active AND publish_at >= $1 AND publish_at < $2
		ORDER BY publish_at, title`, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanTask)
}

func (s *Store) GetTask(ctx context.Context, id string) (Task, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = $1`, id)
	if err != nil {
		return Task{}, err
	}
	t, err := pgx.CollectExactlyOneRow(rows, scanTask)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return t, err
}

// YouGileNames returns the names of the given YouGile users, by ID.
func (s *Store) YouGileNames(ctx context.Context, ids []string) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, real_name FROM yougile_users WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	var id, name string
	_, err = pgx.ForEachRow(rows, []any{&id, &name}, func() error {
		names[id] = name
		return nil
	})
	return names, err
}

// The Advance* methods claim a step before its message is sent: each returns
// false if the step is no longer due, e.g. a retried job already took it.

// AdvanceMaterial moves the material step from step to step+1, unless the
// material has come.
func (s *Store) AdvanceMaterial(ctx context.Context, id string, step int) (bool, error) {
	return s.claim(ctx, `
		UPDATE tasks SET material_step = $2 + 1, material_step_at = now()
		WHERE id = $1 AND active AND material_step = $2 AND material_received_at IS NULL`, id, step)
}

// AdvanceReminders records that reminders up to n were sent; earlier ones
// that are overdue are skipped.
func (s *Store) AdvanceReminders(ctx context.Context, id string, sent, n int) (bool, error) {
	return s.claim(ctx, `
		UPDATE tasks SET reminders_sent = $3
		WHERE id = $1 AND active AND status = 'pending' AND reminders_sent = $2`, id, sent, n)
}

// AdvancePublishAttempt moves the "published?" question from attempt to attempt+1.
func (s *Store) AdvancePublishAttempt(ctx context.Context, id string, attempt int) (bool, error) {
	return s.claim(ctx, `
		UPDATE tasks SET attempts = $2 + 1, asked_at = now()
		WHERE id = $1 AND active AND status = 'pending' AND attempts = $2`, id, attempt)
}

// CloseUnanswered records "no answer" after the last question.
func (s *Store) CloseUnanswered(ctx context.Context, id string, attempts int) (bool, error) {
	return s.claim(ctx, `
		UPDATE tasks SET status = 'no_answer'
		WHERE id = $1 AND status = 'pending' AND attempts = $2`, id, attempts)
}

// The Release* functions undo a claim whose message did not go, so the step
// is taken again. Each only applies if nothing has moved the task since.

// ReleaseMaterial puts the material step back from step+1 to step.
func (s *Store) ReleaseMaterial(ctx context.Context, id string, step int, prevAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE tasks SET material_step = $2, material_step_at = $3
		WHERE id = $1 AND material_step = $2 + 1 AND material_received_at IS NULL`, id, step, nullTime(prevAt))
	return err
}

// ReleaseReminders puts the sent reminders back from n to sent.
func (s *Store) ReleaseReminders(ctx context.Context, id string, sent, n int) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE tasks SET reminders_sent = $2
		WHERE id = $1 AND status = 'pending' AND reminders_sent = $3`, id, sent, n)
	return err
}

// ReleasePublishAttempt puts the "published?" attempt back from attempt+1.
func (s *Store) ReleasePublishAttempt(ctx context.Context, id string, attempt int, prevAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE tasks SET attempts = $2, asked_at = $3
		WHERE id = $1 AND status = 'pending' AND attempts = $2 + 1`, id, attempt, nullTime(prevAt))
	return err
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (s *Store) claim(ctx context.Context, sql string, args ...any) (bool, error) {
	tag, err := s.pool.Exec(ctx, sql, args...)
	return tag.RowsAffected() == 1, err
}

// AnswerPublished records a "published?" answer on the current attempt. As in
// the sales check, "yes" is final: a later "no" does not undo it, and the
// returned task then still has PublishDone. Answers after "no answer" count.
func (s *Store) AnswerPublished(ctx context.Context, id string, published bool) (Task, error) {
	status := PublishNotDone
	if published {
		status = PublishDone
	}
	// In SET, status refers to the value before the update.
	rows, err := s.pool.Query(ctx, `
		UPDATE tasks
		SET status           = CASE WHEN status = 'published' THEN status ELSE $2 END,
		    answered_attempt = CASE WHEN status = 'published' THEN answered_attempt ELSE attempts END,
		    answered_at      = CASE WHEN status = 'published' THEN answered_at ELSE now() END
		WHERE id = $1
		RETURNING `+taskColumns, id, status)
	if err != nil {
		return Task{}, err
	}
	t, err := pgx.CollectExactlyOneRow(rows, scanTask)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return t, err
}

// ReceiveMaterial records that the material came from tgID. first is false if
// it had come already.
func (s *Store) ReceiveMaterial(ctx context.Context, id string, tgID int64) (t Task, first bool, err error) {
	first, err = s.claim(ctx, `
		UPDATE tasks SET material_received_at = now(), material_from = $2
		WHERE id = $1 AND material_received_at IS NULL`, id, tgID)
	if err != nil {
		return Task{}, false, err
	}
	t, err = s.GetTask(ctx, id)
	return t, first, err
}

// AddTaskMessage remembers a message the bot sent about the task.
func (s *Store) AddTaskMessage(ctx context.Context, chatID int64, messageID int, taskID, kind string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO task_messages (chat_id, message_id, task_id, kind) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, chatID, messageID, taskID, kind)
	return err
}

// TaskForMessage returns the task a bot message was about, and its kind.
func (s *Store) TaskForMessage(ctx context.Context, chatID int64, messageID int) (taskID, kind string, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT task_id, kind FROM task_messages WHERE chat_id = $1 AND message_id = $2`,
		chatID, messageID).Scan(&taskID, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return taskID, kind, err
}

// CountActiveTasks counts the active tasks published within [from, to).
func (s *Store) CountActiveTasks(ctx context.Context, from, to time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM tasks WHERE active AND publish_at >= $1 AND publish_at < $2`,
		from, to).Scan(&n)
	return n, err
}
