package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

type Role string

// RoleSuperadmin is not stored in the database: the superadmin is whoever has
// SUPERADMIN_TG_ID from the config.
const (
	RoleSuperadmin Role = "superadmin"
	RoleAdmin      Role = "admin"
	RoleSales      Role = "sales"
	RoleMember     Role = "member"
)

type User struct {
	TgID      int64
	Username  string
	FirstName string
	Role      Role
}

var ErrNotFound = errors.New("not found")

// TouchUser creates the user on first contact and refreshes the name fields
// afterwards. The role is never changed here. created reports whether the
// user is new.
func (s *Store) TouchUser(ctx context.Context, u User) (created bool, err error) {
	// xmax = 0 only for a freshly inserted row, not for an updated one.
	err = s.pool.QueryRow(ctx, `
		INSERT INTO users (tg_id, username, first_name)
		VALUES ($1, NULLIF($2, ''), $3)
		ON CONFLICT (tg_id) DO UPDATE
		SET username   = EXCLUDED.username,
		    first_name = EXCLUDED.first_name,
		    updated_at = now()
		RETURNING (xmax = 0)`,
		u.TgID, u.Username, u.FirstName).Scan(&created)
	return created, err
}

func (s *Store) GetUser(ctx context.Context, tgID int64) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		SELECT tg_id, COALESCE(username, ''), first_name, role
		FROM users WHERE tg_id = $1`, tgID).
		Scan(&u.TgID, &u.Username, &u.FirstName, &u.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// ListUsers returns users with the given role, most recently seen first.
func (s *Store) ListUsers(ctx context.Context, role Role, limit int) ([]User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tg_id, COALESCE(username, ''), first_name, role
		FROM users WHERE role = $1
		ORDER BY updated_at DESC
		LIMIT $2`, role, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanUser)
}

func scanUser(row pgx.CollectableRow) (User, error) {
	var u User
	err := row.Scan(&u.TgID, &u.Username, &u.FirstName, &u.Role)
	return u, err
}

// MarkStarted records that the user wrote /start to the bot in private.
func (s *Store) MarkStarted(ctx context.Context, tgID int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET started_at = now() WHERE tg_id = $1`, tgID)
	return err
}

// ListAdminCandidates returns regular users who have started the bot, so the
// bot can notify them when they are promoted.
func (s *Store) ListAdminCandidates(ctx context.Context, excludeID int64, limit int) ([]User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tg_id, COALESCE(username, ''), first_name, role
		FROM users
		WHERE role = 'member' AND started_at IS NOT NULL AND tg_id <> $1
		ORDER BY started_at DESC
		LIMIT $2`, excludeID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanUser)
}

// SetRole changes the user's role. Losing the admin role also drops the
// primary admin mark; the first admin becomes primary automatically.
func (s *Store) SetRole(ctx context.Context, tgID int64, role Role) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE users
		SET role = $2,
		    primary_admin = $2 = 'admin' AND (primary_admin OR NOT EXISTS (
		        SELECT 1 FROM users WHERE primary_admin AND tg_id <> $1)),
		    updated_at = now()
		WHERE tg_id = $1`, tgID, role)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPrimaryAdmin makes the admin the primary one, replacing the previous.
func (s *Store) SetPrimaryAdmin(ctx context.Context, tgID int64) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE users SET primary_admin = false WHERE primary_admin`); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE users SET primary_admin = true WHERE tg_id = $1 AND role = 'admin'`, tgID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// PrimaryAdmin returns the primary admin or ErrNotFound.
func (s *Store) PrimaryAdmin(ctx context.Context) (User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tg_id, COALESCE(username, ''), first_name, role
		FROM users WHERE primary_admin AND role = 'admin'`)
	if err != nil {
		return User{}, err
	}
	u, err := pgx.CollectExactlyOneRow(rows, scanUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}
