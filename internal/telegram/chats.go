package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"remindbot/internal/storage"
)

// Callback data for chat purposes: "chat:<chat_id>:content|sales|none".
const (
	cbChatPrefix = "chat:"
	cbChatNone   = "none"
)

var purposeLabels = map[storage.ChatPurpose]string{
	storage.PurposeMaterial: "📎 Материалы",
	storage.PurposePublish:  "🔔 Выкладка",
	storage.PurposeSales:    "💼 Продажи",
	// The old single content chat: still works, gets both; not offered anew.
	storage.PurposeContent: "📝 Контент (материалы + выкладка)",
	storage.PurposeNone:    "— не назначено",
}

// onBotAdded runs when the bot is added to a group. Only the superadmin and
// admins may add it; otherwise the bot leaves and warns the superadmin.
func (t *Bot) onBotAdded(ctx context.Context, upd *models.ChatMemberUpdated) {
	chat := upd.Chat
	by := storage.User{TgID: upd.From.ID, Username: upd.From.Username, FirstName: upd.From.FirstName}

	if !isAdmin(t.roleOf(ctx, by.TgID)) {
		if _, err := t.api.LeaveChat(ctx, &bot.LeaveChatParams{ChatID: chat.ID}); err != nil {
			t.log.Error("leave chat", "chat_id", chat.ID, "err", err)
		}
		t.log.Warn("added to a chat by a stranger, left", "chat_id", chat.ID, "by", by.TgID)
		if t.superadminID != 0 {
			t.send(ctx, t.superadminID, fmt.Sprintf(
				"⚠️ %s добавил бота в чат «%s». Это не админ — бот вышел из чата.",
				displayName(by), html.EscapeString(chat.Title)), nil)
		}
		return
	}

	// The question goes to the superadmin and to the admin who added the
	// bot: either of them can answer.
	text := fmt.Sprintf("✅ %s добавил бота в чат «%s».\nДля чего этот чат?",
		displayName(by), html.EscapeString(chat.Title))
	var to []int64
	if t.superadminID != 0 {
		to = append(to, t.superadminID)
	}
	if by.TgID != t.superadminID {
		to = append(to, by.TgID)
	}
	for _, id := range to {
		t.send(ctx, id, text, purposeButtons(chat.ID))
	}
}

func (t *Bot) onChats(ctx context.Context, _ *bot.Bot, update *models.Update) {
	text, markup, err := t.chatsScreen(ctx)
	if err != nil {
		t.log.Error("chats screen", "err", err)
		t.send(ctx, update.Message.Chat.ID, "Не удалось загрузить чаты, попробуй ещё раз.", nil)
		return
	}
	t.send(ctx, update.Message.Chat.ID, text, markup)
}

func (t *Bot) chatsScreen(ctx context.Context) (string, models.ReplyMarkup, error) {
	chats, err := t.store.ListChats(ctx)
	if err != nil {
		return "", nil, err
	}
	var sb strings.Builder
	sb.WriteString("💬 <b>Групповые чаты</b>\n")
	if len(chats) == 0 {
		sb.WriteString("\nБот пока не добавлен ни в один чат. Добавь его в группу — я спрошу, для чего она.")
		return sb.String(), nil, nil
	}

	var rows [][]models.InlineKeyboardButton
	for _, c := range chats {
		fmt.Fprintf(&sb, "\n<b>%s</b> — %s", html.EscapeString(c.Title), purposeLabels[c.Purpose])
		rows = append(rows, []models.InlineKeyboardButton{{Text: "«" + c.Title + "»:", CallbackData: "noop"}})
		rows = append(rows, purposeButtons(c.ID).InlineKeyboard...)
	}
	sb.WriteString("\n\n📎 Материалы — «пришли материал» за сутки и напоминание через час." +
		"\n🔔 Выкладка — напоминания о публикации и контент-план по понедельникам." +
		"\n💼 Продажи — напоминание заполнить отчёт (без чата — в личку).")
	return sb.String(), &models.InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

func purposeButtons(chatID int64) *models.InlineKeyboardMarkup {
	data := func(p string) string { return fmt.Sprintf("%s%d:%s", cbChatPrefix, chatID, p) }
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: purposeLabels[storage.PurposeMaterial], CallbackData: data(string(storage.PurposeMaterial))},
		{Text: purposeLabels[storage.PurposePublish], CallbackData: data(string(storage.PurposePublish))},
	}, {
		{Text: purposeLabels[storage.PurposeSales], CallbackData: data(string(storage.PurposeSales))},
		{Text: "Без назначения", CallbackData: data(cbChatNone)},
	}}}
}

func (t *Bot) onChatCallback(ctx context.Context, b *bot.Bot, update *models.Update) {
	cq := update.CallbackQuery
	answer := func(text string) {
		if _, err := b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID, Text: text}); err != nil {
			t.log.Error("answer callback", "err", err)
		}
	}
	// Admins add the bot to chats, so they assign the chats' roles too.
	if !isAdmin(t.roleOf(ctx, cq.From.ID)) {
		answer("Нет доступа")
		return
	}

	rawID, rawPurpose, _ := strings.Cut(strings.TrimPrefix(cq.Data, cbChatPrefix), ":")
	chatID, err := strconv.ParseInt(rawID, 10, 64)
	purpose := storage.ChatPurpose(rawPurpose)
	if rawPurpose == cbChatNone {
		purpose = storage.PurposeNone
	}
	if _, known := purposeLabels[purpose]; err != nil || !known {
		answer("Некорректная кнопка")
		return
	}

	prev, err := t.store.ChatPurposeOf(ctx, chatID)
	// Without the old purpose it is unknown whether the chat was greeted:
	// then it is not greeted again (/hello does it by hand).
	prevKnown := err == nil || errors.Is(err, storage.ErrNotFound)
	if !prevKnown {
		t.log.Error("chat purpose", "chat_id", chatID, "err", err)
	}
	if err := t.store.SetChatPurpose(ctx, chatID, purpose); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			answer("Чат не найден")
		} else {
			t.log.Error("set chat purpose", "chat_id", chatID, "err", err)
			answer("Ошибка, попробуй ещё раз")
		}
		return
	}
	t.log.Info("chat purpose changed", "chat_id", chatID, "purpose", purpose)
	answer("Готово: " + purposeLabels[purpose])
	// A chat that just got its role is greeted: people learn what the bot
	// does there and start it in one tap.
	if prevKnown && purpose != storage.PurposeNone && purpose != prev {
		t.greetChat(ctx, chatID, purpose)
	}

	// Redraw as the chats screen: it shows the result for every chat.
	if m := cq.Message.Message; m != nil {
		text, markup, err := t.chatsScreen(ctx)
		if err != nil {
			t.log.Error("chats screen", "err", err)
			return
		}
		if _, err := t.api.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID: m.Chat.ID, MessageID: m.ID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: markup,
		}); err != nil {
			t.log.Error("edit chats screen", "err", err)
		}
	}
}

// onNoop answers buttons that only label a row.
func (t *Bot) onNoop(ctx context.Context, b *bot.Bot, update *models.Update) {
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: update.CallbackQuery.ID})
}
