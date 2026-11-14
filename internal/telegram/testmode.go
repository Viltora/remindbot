package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/go-telegram/bot/models"

	"remindbot/internal/storage"
)

// Test mode (TEST_RECIPIENT_TG_ID, one or more IDs) checks the bot in
// production without bothering the team: every scheduled notification goes to
// the test recipients' DMs instead of the people and chats it is meant for,
// marked 🧪, each recipient getting their own copy. In the sales report check
// the recipients play the sales users, each with their own question; the
// content chat — the content plan and the YouGile publications — is their DMs.
// Replies to commands and the superadmin's notices about new people and
// chats are not affected.

const testMark = "🧪 "

func (t *Bot) testMode() bool {
	return len(t.testRecipients) > 0
}

// checkTestRecipients makes sure the bot knows the test recipients, so test
// mode does not fail later, at the first scheduled send.
func checkTestRecipients(ctx context.Context, store *storage.Store, ids []int64, log *slog.Logger) error {
	var names []string
	for _, id := range ids {
		u, err := store.GetUser(ctx, id)
		if errors.Is(err, storage.ErrNotFound) {
			return fmt.Errorf("TEST_RECIPIENT_TG_ID: пользователь %d не найден — он должен сначала написать боту /start", id)
		}
		if err != nil {
			return fmt.Errorf("TEST_RECIPIENT_TG_ID: %w", err)
		}
		names = append(names, u.Username)
	}
	if len(ids) > 0 {
		log.Warn("test mode: scheduled notifications go to the test recipients only",
			"tg_ids", ids, "usernames", names)
	}
	return nil
}

// notify sends a scheduled notification and returns what was sent. In test
// mode a message for a test recipient goes to them, and any other goes to all
// the test recipients: the scenarios already address them, and this makes sure
// nobody else gets a test message.
func (t *Bot) notify(ctx context.Context, chatID int64, html string, markup models.ReplyMarkup) []*models.Message {
	to := []int64{chatID}
	if t.testMode() {
		html = testMark + html
		if !slices.Contains(t.testRecipients, chatID) {
			t.log.Info("test mode: redirected to the test recipients", "meant_for", chatID)
			to = t.testRecipients
		}
	}
	var sent []*models.Message
	for _, id := range to {
		if m := t.send(ctx, id, html, markup); m != nil {
			sent = append(sent, m)
		}
	}
	return sent
}

// contentChats returns where content notifications go: the content chats, or
// in test mode the test recipients' DMs.
func (t *Bot) contentChats(ctx context.Context) ([]int64, error) {
	if t.testMode() {
		return t.testRecipients, nil
	}
	return t.store.ChatsFor(ctx, storage.PurposeContent)
}

// salesUsers returns who is asked for the daily report: the sales users, or
// in test mode the test recipients.
func (t *Bot) salesUsers(ctx context.Context) ([]storage.User, error) {
	if !t.testMode() {
		return t.store.ListUsers(ctx, storage.RoleSales, 1000)
	}
	users := make([]storage.User, 0, len(t.testRecipients))
	for _, id := range t.testRecipients {
		u, err := t.store.GetUser(ctx, id)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, nil
}

// adminIDs returns who gets summaries meant for the admins: the admins, or in
// test mode the test recipients.
func (t *Bot) adminIDs(ctx context.Context) ([]int64, error) {
	if t.testMode() {
		return t.testRecipients, nil
	}
	admins, err := t.store.ListUsers(ctx, storage.RoleAdmin, 1000)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(admins))
	for i, a := range admins {
		ids[i] = a.TgID
	}
	return ids, nil
}

// testModeStatus is the /status line about test mode, "" when it is off.
func (t *Bot) testModeStatus(ctx context.Context) string {
	if !t.testMode() {
		return ""
	}
	who := make([]string, len(t.testRecipients))
	for i, id := range t.testRecipients {
		who[i] = fmt.Sprintf("<code>%d</code>", id)
		if u, err := t.store.GetUser(ctx, id); err == nil {
			who[i] = displayName(u)
		}
	}
	return testMark + "Тестовый режим: уведомления по расписанию получают только " + strings.Join(who, ", ") + "\n"
}
