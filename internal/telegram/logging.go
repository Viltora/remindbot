package telegram

import (
	"context"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// logUpdate logs every update the bot gets: commands, button presses, replies,
// people joining and the bot being added to chats. Free text is never logged,
// only what kind of content a message has.
func (t *Bot) logUpdate(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		switch {
		case update.Message != nil:
			m := update.Message
			attrs := []any{"chat_id", m.Chat.ID, "chat_type", m.Chat.Type, "message_id", m.ID}
			if m.From != nil {
				attrs = append(attrs, "from", m.From.ID, "username", m.From.Username)
			}
			if cmd := commandOf(m); cmd != "" {
				attrs = append(attrs, "command", cmd)
			}
			if m.ReplyToMessage != nil {
				attrs = append(attrs, "reply_to", m.ReplyToMessage.ID)
			}
			attrs = append(attrs, "content", contentKinds(m))
			t.log.Info("update: message", attrs...)
		case update.CallbackQuery != nil:
			cq := update.CallbackQuery
			attrs := []any{"from", cq.From.ID, "username", cq.From.Username, "data", cq.Data}
			if m := cq.Message.Message; m != nil {
				attrs = append(attrs, "chat_id", m.Chat.ID, "message_id", m.ID)
			}
			t.log.Info("update: button", attrs...)
		case update.MyChatMember != nil:
			u := update.MyChatMember
			t.log.Info("update: bot membership", "chat_id", u.Chat.ID, "title", u.Chat.Title,
				"by", u.From.ID, "status", u.NewChatMember.Type)
		default:
			t.log.Debug("update: other", "update_id", update.ID)
		}
		next(ctx, b, update)
	}
}

// commandOf returns "/name" if the message starts with a bot command.
func commandOf(m *models.Message) string {
	for _, e := range m.Entities {
		if e.Type == models.MessageEntityTypeBotCommand && e.Offset == 0 && e.Length <= len(m.Text) {
			return m.Text[:e.Length]
		}
	}
	return ""
}

// contentKinds says what a message carries, e.g. "photo,link", without the
// text itself.
func contentKinds(m *models.Message) string {
	var kinds []string
	add := func(ok bool, kind string) {
		if ok {
			kinds = append(kinds, kind)
		}
	}
	add(m.Text != "", "text")
	add(len(m.Photo) > 0, "photo")
	add(m.Video != nil, "video")
	add(m.Document != nil, "document")
	add(m.Animation != nil, "animation")
	add(m.Audio != nil, "audio")
	add(m.Voice != nil, "voice")
	add(m.VideoNote != nil, "video_note")
	add(m.Sticker != nil, "sticker")
	add(hasLink(m), "link")
	add(m.MediaGroupID != "", "album")
	add(len(m.NewChatMembers) > 0, "joined")
	add(m.LeftChatMember != nil, "left")
	if len(kinds) == 0 {
		return "other"
	}
	return strings.Join(kinds, ",")
}

// preview shortens a message text for the log: one line, at most 120 runes.
func preview(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return text
}
