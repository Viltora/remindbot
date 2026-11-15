package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// YouGilePerson is a YouGile user who is or was an assignee of a tracked
// task, and the Telegram user linked to them, if any (User.TgID 0 = none).
type YouGilePerson struct {
	ID   string
	Name string // real name from YouGile; may be empty or an e-mail
	User User
}

// Assignees returns the YouGile people assigned to the tracked tasks, by
// name, with their linked Telegram users.
func (s *Store) Assignees(ctx context.Context) ([]YouGilePerson, error) {
	rows, err := s.pool.Query(ctx, personSelect+`
		WHERE y.id IN (SELECT unnest(assignees) FROM tasks)
		ORDER BY lower(y.real_name), y.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanPerson)
}

// personSelect reads YouGile people with their linked Telegram users, in
// the order scanPerson expects; callers add WHERE and ORDER BY.
const personSelect = `
	SELECT y.id, y.real_name,
	       COALESCE(u.tg_id, 0), COALESCE(u.username, ''), COALESCE(u.first_name, ''), COALESCE(u.role, '')
	FROM yougile_users y
	LEFT JOIN users u ON u.tg_id = y.tg_id`

func scanPerson(row pgx.CollectableRow) (YouGilePerson, error) {
	var p YouGilePerson
	err := row.Scan(&p.ID, &p.Name, &p.User.TgID, &p.User.Username, &p.User.FirstName, &p.User.Role)
	return p, err
}

// AssigneeUsers returns the Telegram users linked to the given YouGile users,
// by YouGile ID; unlinked ones are absent.
func (s *Store) AssigneeUsers(ctx context.Context, ids []string) (map[string]User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT y.id, u.tg_id, COALESCE(u.username, ''), u.first_name, u.role
		FROM yougile_users y JOIN users u ON u.tg_id = y.tg_id
		WHERE y.id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	users := map[string]User{}
	var id string
	var u User
	_, err = pgx.ForEachRow(rows, []any{&id, &u.TgID, &u.Username, &u.FirstName, &u.Role}, func() error {
		users[id] = u
		return nil
	})
	return users, err
}

// LinkedYouGile returns the YouGile person linked to the Telegram user;
// ok is false if there is none.
func (s *Store) LinkedYouGile(ctx context.Context, tgID int64) (p YouGilePerson, ok bool, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT id, real_name FROM yougile_users WHERE tg_id = $1 LIMIT 1`, tgID).Scan(&p.ID, &p.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return YouGilePerson{}, false, nil
	}
	return p, err == nil, err
}

// LinkYouGile links a YouGile person to a Telegram user. A person has at most
// one Telegram user and the other way round: earlier links of either are
// dropped. ygID "" only drops the user's link.
func (s *Store) LinkYouGile(ctx context.Context, tgID int64, ygID string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE yougile_users SET tg_id = NULL, updated_at = now() WHERE tg_id = $1`, tgID); err != nil {
			return err
		}
		if ygID == "" {
			return nil
		}
		tag, err := tx.Exec(ctx, `
			UPDATE yougile_users SET tg_id = $1, updated_at = now() WHERE id = $2`, tgID, ygID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ListPeople returns everyone the bot knows: who wrote /start or was seen in
// a group, the superadmin (set in the config, not stored as a role) first,
// then admins, sales and the rest.
func (s *Store) ListPeople(ctx context.Context, superadminID int64, limit int) ([]User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tg_id, COALESCE(username, ''), first_name, role
		FROM users
		ORDER BY tg_id = $1 DESC,
		         CASE role WHEN 'admin' THEN 1 WHEN 'sales' THEN 2 ELSE 3 END,
		         started_at IS NULL, lower(COALESCE(NULLIF(username, ''), first_name))
		LIMIT $2`, superadminID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanUser)
}

// GetPerson returns the user and whether they have pressed Start in a
// private chat with the bot, in one query.
func (s *Store) GetPerson(ctx context.Context, tgID int64) (u User, started bool, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT tg_id, COALESCE(username, ''), first_name, role, started_at IS NOT NULL
		FROM users WHERE tg_id = $1`, tgID).
		Scan(&u.TgID, &u.Username, &u.FirstName, &u.Role, &started)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, ErrNotFound
	}
	return u, started, err
}

// YouGileOwner returns the YouGile person ygID with the Telegram user linked
// to it (User.TgID 0 = none).
func (s *Store) YouGileOwner(ctx context.Context, ygID string) (YouGilePerson, error) {
	rows, err := s.pool.Query(ctx, personSelect+`
		WHERE y.id = $1`, ygID)
	if err != nil {
		return YouGilePerson{}, err
	}
	p, err := pgx.CollectExactlyOneRow(rows, scanPerson)
	if errors.Is(err, pgx.ErrNoRows) {
		return YouGilePerson{}, ErrNotFound
	}
	return p, err
}

// ChatPurposeOf returns the chat's purpose (PurposeNone if not assigned).
func (s *Store) ChatPurposeOf(ctx context.Context, chatID int64) (ChatPurpose, error) {
	var p ChatPurpose
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(purpose, '') FROM chats WHERE chat_id = $1`, chatID).Scan(&p)
	if errors.Is(err, pgx.ErrNoRows) {
		return PurposeNone, ErrNotFound
	}
	return p, err
}
