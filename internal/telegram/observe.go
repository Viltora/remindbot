package telegram

import (
	"context"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"remindbot/internal/storage"
)

// observeGroups records group chats and the people seen in them before the
// update reaches its handler. Telegram does not let bots list chat members,
// so the bot learns about people from what they do in the chat.
func (t *Bot) observeGroups(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		switch {
		case update.MyChatMember != nil:
			t.observeBotMembership(ctx, update.MyChatMember)
		case update.Message != nil:
			t.observeGroupMessage(ctx, update.Message)
		case update.CallbackQuery != nil && update.CallbackQuery.Message.Message != nil:
			msg := update.CallbackQuery.Message.Message
			if isGroup(msg.Chat) {
				t.observePerson(ctx, msg.Chat, update.CallbackQuery.From, true)
			}
		}
		next(ctx, b, update)
	}
}

// observeBotMembership tracks the bot being added to or removed from a group.
func (t *Bot) observeBotMembership(ctx context.Context, upd *models.ChatMemberUpdated) {
	if !isGroup(upd.Chat) {
		return
	}
	active := isPresent(upd.NewChatMember.Type)
	if err := t.store.UpsertChat(ctx, upd.Chat.ID, upd.Chat.Title, active); err != nil {
		t.log.Error("save chat", "chat_id", upd.Chat.ID, "err", err)
		return
	}
	t.log.Info("bot membership changed", "chat_id", upd.Chat.ID, "active", active, "by", upd.From.ID)
	if active && !isPresent(upd.OldChatMember.Type) {
		t.onBotAdded(ctx, upd)
	}
}

func (t *Bot) observeGroupMessage(ctx context.Context, msg *models.Message) {
	if !isGroup(msg.Chat) {
		return
	}
	// Registers chats the bot was added to before it started tracking them.
	if err := t.store.EnsureChat(ctx, msg.Chat.ID, msg.Chat.Title); err != nil {
		t.log.Error("save chat", "chat_id", msg.Chat.ID, "err", err)
		return
	}
	if msg.From != nil {
		t.observePerson(ctx, msg.Chat, *msg.From, true)
	}
	for _, u := range msg.NewChatMembers {
		t.observePerson(ctx, msg.Chat, u, true)
	}
	if msg.LeftChatMember != nil {
		t.observePerson(ctx, msg.Chat, *msg.LeftChatMember, false)
	}
}

func (t *Bot) observePerson(ctx context.Context, chat models.Chat, u models.User, present bool) {
	if u.IsBot {
		return
	}
	err := t.store.SetChatMember(ctx, chat.ID, storage.User{
		TgID:      u.ID,
		Username:  u.Username,
		FirstName: u.FirstName,
	}, present)
	if err != nil {
		t.log.Error("save chat member", "chat_id", chat.ID, "tg_id", u.ID, "err", err)
	}
}

func isGroup(c models.Chat) bool {
	return c.Type == models.ChatTypeGroup || c.Type == models.ChatTypeSupergroup
}

func isPresent(status models.ChatMemberType) bool {
	switch status {
	case models.ChatMemberTypeOwner, models.ChatMemberTypeAdministrator,
		models.ChatMemberTypeMember, models.ChatMemberTypeRestricted:
		return true
	}
	return false
}
