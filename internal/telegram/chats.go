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

// Callback data for the chats screens: "chat:<chat_id>:<purpose>|none" sets
// the purpose, "chat:<chat_id>:open" opens one chat, "chat:0:list" goes
// back to the list.
const (
	cbChatPrefix = "chat:"
	cbChatNone   = "none"
	cbChatOpen   = "open"
	cbChatList   = "list"
)

var purposeLabels = map[storage.ChatPurpose]string{
	storage.PurposeMaterial: "📎 Материалы",
	storage.PurposePublish:  "🔔 Выкладка",
	storage.PurposeSales:    "💼 Продажи",
	storage.PurposeTelegram: "✈️ Телеграм",
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

	// The superadmin is asked, so there is one answer; the admin who added
	// the bot is told, and can set the role in /chats. With no superadmin
	// configured, the admin is asked instead.
	title := html.EscapeString(chat.Title)
	if t.superadminID != 0 {
		t.send(ctx, t.superadminID, fmt.Sprintf("✅ %s добавил бота в чат «%s».\nДля чего этот чат?",
			displayName(by), title), purposeButtons(chat.ID))
	}
	// An admin who never pressed Start cannot be written to: sendToPerson
	// only logs that, without an alert.
	switch {
	case by.TgID == t.superadminID:
	case t.superadminID == 0:
		t.sendToPerson(ctx, by.TgID, fmt.Sprintf("✅ Бот добавлен в чат «%s».\nДля чего этот чат?", title), purposeButtons(chat.ID))
	default:
		t.sendToPerson(ctx, by.TgID, fmt.Sprintf(
			"✅ Бот добавлен в чат «%s».\nРоль чату назначит суперадмин — или задай её сам в /chats.", title), nil)
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

// chatsScreen lists the chats with their purpose, a button per chat; the
// purpose is changed in the chat's own screen, so the list stays short.
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
		rows = append(rows, []models.InlineKeyboardButton{{
			Text: "✏️ " + c.Title, CallbackData: fmt.Sprintf("%s%d:%s", cbChatPrefix, c.ID, cbChatOpen),
		}})
	}
	sb.WriteString("\n\n" + purposesHelp)
	return sb.String(), &models.InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

// purposesHelp says what each purpose gets.
const purposesHelp = "📎 Материалы — «пришли материал» за сутки, один раз." +
	"\n🔔 Выкладка — напоминания о публикации и контент-план по понедельникам." +
	"\n💼 Продажи — напоминание заполнить отчёт (без чата — в личку)." +
	"\n✈️ Телеграм — посты в телеграме на день, за час до первого (без чата — в выкладку)."

// chatScreen is one chat: its purpose and the buttons to change it.
func (t *Bot) chatScreen(ctx context.Context, chatID int64) (string, models.ReplyMarkup, error) {
	chats, err := t.store.ListChats(ctx)
	if err != nil {
		return "", nil, err
	}
	for _, c := range chats {
		if c.ID != chatID {
			continue
		}
		text := fmt.Sprintf("💬 <b>%s</b>\nСейчас: %s\n\nДля чего этот чат?\n\n%s",
			html.EscapeString(c.Title), purposeLabels[c.Purpose], purposesHelp)
		markup := purposeButtons(chatID)
		markup.InlineKeyboard = append(markup.InlineKeyboard, []models.InlineKeyboardButton{{
			Text: "← Все чаты", CallbackData: cbChatPrefix + "0:" + cbChatList,
		}})
		return text, markup, nil
	}
	return "", nil, storage.ErrNotFound
}

func purposeButtons(chatID int64) *models.InlineKeyboardMarkup {
	data := func(p string) string { return fmt.Sprintf("%s%d:%s", cbChatPrefix, chatID, p) }
	button := func(p storage.ChatPurpose) models.InlineKeyboardButton {
		return models.InlineKeyboardButton{Text: purposeLabels[p], CallbackData: data(string(p))}
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{button(storage.PurposeMaterial), button(storage.PurposePublish)},
		{button(storage.PurposeSales), button(storage.PurposeTelegram)},
		{{Text: "Без назначения", CallbackData: data(cbChatNone)}},
	}}
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
	switch {
	case err == nil && rawPurpose == cbChatList:
		answer("")
		t.redrawChats(ctx, cq.Message, func() (string, models.ReplyMarkup, error) { return t.chatsScreen(ctx) })
		return
	case err == nil && rawPurpose == cbChatOpen:
		answer("")
		t.redrawChats(ctx, cq.Message, func() (string, models.ReplyMarkup, error) { return t.chatScreen(ctx, chatID) })
		return
	}
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

	// Redraw as the chats list: it shows the result for every chat.
	t.redrawChats(ctx, cq.Message, func() (string, models.ReplyMarkup, error) { return t.chatsScreen(ctx) })
}

// redrawChats replaces the message the button was pressed on with screen.
func (t *Bot) redrawChats(ctx context.Context, msg models.MaybeInaccessibleMessage, screen func() (string, models.ReplyMarkup, error)) {
	m := msg.Message
	if m == nil {
		return // too old to edit
	}
	text, markup, err := screen()
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

// onNoop answers buttons that only label a row.
func (t *Bot) onNoop(ctx context.Context, b *bot.Bot, update *models.Update) {
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: update.CallbackQuery.ID})
}
