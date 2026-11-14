package telegram

import (
	"context"
	"errors"
	"html"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"remindbot/internal/storage"
)

// Commands shown in the "Menu" button, per role. Members get no menu.
var (
	adminCommands = []models.BotCommand{
		{Command: "today", Description: "📅 Сегодня"},
		{Command: "week", Description: "📊 Неделя"},
		{Command: "month", Description: "🗓 Месяц"},
	}
	superadminCommands = append(adminCommands[:len(adminCommands):len(adminCommands)],
		models.BotCommand{Command: "people", Description: "👥 Люди: роли и YouGile"},
		models.BotCommand{Command: "admins", Description: "🛡 Роли: админы и sales"},
		models.BotCommand{Command: "chats", Description: "💬 Чаты: материалы, выкладка, продажи"},
		models.BotCommand{Command: "status", Description: "ℹ️ Статус"},
		models.BotCommand{Command: "help", Description: "❓ Что умеет бот"},
	)
)

// removeKeyboard clears the reply keyboard left from earlier bot versions.
var removeKeyboard = &models.ReplyKeyboardRemove{RemoveKeyboard: true}

func (t *Bot) roleOf(ctx context.Context, tgID int64) storage.Role {
	if t.superadminID != 0 && tgID == t.superadminID {
		return storage.RoleSuperadmin
	}
	u, err := t.store.GetUser(ctx, tgID)
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			t.log.Error("load user role", "tg_id", tgID, "err", err)
		}
		return storage.RoleMember
	}
	return u.Role
}

func isAdmin(r storage.Role) bool {
	return r == storage.RoleAdmin || r == storage.RoleSuperadmin
}

func isSuperadmin(r storage.Role) bool {
	return r == storage.RoleSuperadmin
}

func (t *Bot) adminOnly(h bot.HandlerFunc) bot.HandlerFunc {
	return t.privateFor(isAdmin, h)
}

func (t *Bot) superadminOnly(h bot.HandlerFunc) bot.HandlerFunc {
	return t.privateFor(isSuperadmin, h)
}

// privateFor runs h for private messages from users whose role passes allowed.
// Group messages are ignored; others get "no access" and lose a stale menu,
// e.g. after being removed from admins.
func (t *Bot) privateFor(allowed func(storage.Role) bool, h bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		msg := update.Message
		if msg == nil || msg.From == nil || msg.Chat.Type != models.ChatTypePrivate {
			return
		}
		role := t.roleOf(ctx, msg.From.ID)
		if !allowed(role) {
			t.send(ctx, msg.Chat.ID, "Нет доступа.", nil)
			t.syncMenu(ctx, msg.From.ID, role)
			return
		}
		h(ctx, b, update)
	}
}

// syncMenu sets the user's personal command menu according to their role.
// Telegram stores it per chat, so it survives bot restarts.
func (t *Bot) syncMenu(ctx context.Context, tgID int64, role storage.Role) {
	scope := &models.BotCommandScopeChat{ChatID: tgID}
	var err error
	switch role {
	case storage.RoleSuperadmin:
		_, err = t.api.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: superadminCommands, Scope: scope})
	case storage.RoleAdmin:
		_, err = t.api.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: adminCommands, Scope: scope})
	default:
		_, err = t.api.DeleteMyCommands(ctx, &bot.DeleteMyCommandsParams{Scope: scope})
	}
	if err != nil {
		t.log.Error("set command menu", "tg_id", tgID, "role", role, "err", err)
	}
}

// setupMenus runs on startup: no menu by default, personal menus for the
// superadmin and admins.
func (t *Bot) setupMenus(ctx context.Context) {
	if _, err := t.api.DeleteMyCommands(ctx, &bot.DeleteMyCommandsParams{}); err != nil {
		t.log.Error("clear default command menu", "err", err)
	}
	if t.superadminID != 0 {
		t.syncMenu(ctx, t.superadminID, storage.RoleSuperadmin)
	}
	admins, err := t.store.ListUsers(ctx, storage.RoleAdmin, 1000)
	if err != nil {
		t.log.Error("load admins for menus", "err", err)
		return
	}
	for _, u := range admins {
		t.syncMenu(ctx, u.TgID, storage.RoleAdmin)
	}
}

// displayName shows people by @username, falling back to the first name.
// The result is HTML-escaped.
func displayName(u storage.User) string {
	if u.Username != "" {
		return "@" + html.EscapeString(u.Username)
	}
	return html.EscapeString(u.FirstName)
}
