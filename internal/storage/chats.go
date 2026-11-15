package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// UpsertChat records a group chat and whether the bot is currently in it.
func (s *Store) UpsertChat(ctx context.Context, chatID int64, title string, active bool) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO chats (chat_id, title, active)
		VALUES ($1, $2, $3)
		ON CONFLICT (chat_id) DO UPDATE
		SET title      = EXCLUDED.title,
		    active     = EXCLUDED.active,
		    updated_at = now()`,
		chatID, title, active)
	return err
}

// EnsureChat registers a chat the bot got a message from, e.g. one it was
// added to before chats were tracked. Unlike UpsertChat it never changes
// whether the bot is in the chat: handlers run concurrently, and a late
// message must not undo the bot leaving.
func (s *Store) EnsureChat(ctx context.Context, chatID int64, title string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO chats (chat_id, title, active)
		VALUES ($1, $2, true)
		ON CONFLICT (chat_id) DO UPDATE
		SET title = EXCLUDED.title, updated_at = now()`,
		chatID, title)
	return err
}

// SetChatMember records that a user is (or is no longer) in a group chat.
// The chat must already exist.
func (s *Store) SetChatMember(ctx context.Context, chatID int64, u User, present bool) error {
	if _, err := s.TouchUser(ctx, u); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO chat_members (chat_id, tg_id, present)
		VALUES ($1, $2, $3)
		ON CONFLICT (chat_id, tg_id) DO UPDATE
		SET present    = EXCLUDED.present,
		    updated_at = now()`,
		chatID, u.TgID, present)
	return err
}

type ChatPurpose string

const (
	PurposeNone ChatPurpose = ""
	// PurposeContent is the old single content chat: it gets what goes to
	// both PurposeMaterial and PurposePublish.
	PurposeContent  ChatPurpose = "content"
	PurposeSales    ChatPurpose = "sales"
	PurposeMaterial ChatPurpose = "material" // requests for material for approval
	PurposePublish  ChatPurpose = "publish"  // publishing reminders, content plan
)

type Chat struct {
	ID      int64
	Title   string
	Purpose ChatPurpose
}

// ListChats returns the group chats the bot is currently in.
func (s *Store) ListChats(ctx context.Context) ([]Chat, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT chat_id, title, COALESCE(purpose, '') FROM chats
		WHERE active ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Chat, error) {
		var c Chat
		err := row.Scan(&c.ID, &c.Title, &c.Purpose)
		return c, err
	})
}

// ChatsFor returns the active group chats with any of the given purposes.
func (s *Store) ChatsFor(ctx context.Context, purposes ...ChatPurpose) ([]int64, error) {
	names := make([]string, len(purposes))
	for i, p := range purposes {
		names[i] = string(p)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT chat_id FROM chats WHERE active AND purpose = ANY($1) ORDER BY created_at`, names)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

func (s *Store) SetChatPurpose(ctx context.Context, chatID int64, purpose ChatPurpose) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE chats SET purpose = NULLIF($2, ''), updated_at = now() WHERE chat_id = $1`,
		chatID, string(purpose))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type Stats struct {
	Users       int // everyone the bot knows, the superadmin included
	Admins      int
	Sales       int
	Telegram    int
	Regular     int // no role
	InGroups    int // seen in a group chat where the bot is now
	ActiveChats int
	// MaterialChats and PublishChats count the old all-content chats too.
	MaterialChats int
	PublishChats  int
	SalesChats    int
	Unassigned    int
}

func (s *Store) Stats(ctx context.Context, superadminID int64) (Stats, error) {
	var st Stats
	err := s.pool.QueryRow(ctx, `
		SELECT
		    (SELECT count(*) FROM users),
		    (SELECT count(*) FROM users WHERE role = 'admin' AND tg_id <> $1),
		    (SELECT count(*) FROM users WHERE role = 'sales' AND tg_id <> $1),
		    (SELECT count(*) FROM users WHERE role = 'telegram' AND tg_id <> $1),
		    (SELECT count(*) FROM users WHERE role = 'member' AND tg_id <> $1),
		    (SELECT count(DISTINCT m.tg_id)
		       FROM chat_members m JOIN chats c USING (chat_id)
		      WHERE m.present AND c.active),
		    (SELECT count(*) FROM chats WHERE active),
		    (SELECT count(*) FROM chats WHERE active AND purpose IN ('material', 'content')),
		    (SELECT count(*) FROM chats WHERE active AND purpose IN ('publish', 'content')),
		    (SELECT count(*) FROM chats WHERE active AND purpose = 'sales'),
		    (SELECT count(*) FROM chats WHERE active AND purpose IS NULL)`,
		superadminID).
		Scan(&st.Users, &st.Admins, &st.Sales, &st.Telegram, &st.Regular, &st.InGroups,
			&st.ActiveChats, &st.MaterialChats, &st.PublishChats, &st.SalesChats, &st.Unassigned)
	return st, err
}
