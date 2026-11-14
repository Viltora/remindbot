package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"remindbot/internal/storage"
	"remindbot/internal/version"
)

// Callback data for the roles screen: "adm:<action>:<tg_id>".
const (
	cbAdminPrefix = "adm:"
	cbAdminAdd    = "add"   // make admin
	cbAdminSales  = "sales" // make sales
	cbAdminMain   = "main"  // make the primary admin
	cbAdminAsk    = "ask"   // ask to confirm removing the role
	cbAdminDel    = "rm"    // remove the role (confirmed)
	// Old screens sent "del" for an immediate removal; it now asks too.
	cbAdminLegacyDel = "del"
	cbAdminBack      = "back" // cancel: back to the roles screen
)

// How many candidates (users who started the bot) to offer on the admins screen.
const adminCandidatesLimit = 10

func (t *Bot) onAdmins(ctx context.Context, _ *bot.Bot, update *models.Update) {
	text, markup, err := t.adminsScreen(ctx)
	if err != nil {
		t.log.Error("admins screen", "err", err)
		t.send(ctx, update.Message.Chat.ID, "Не удалось загрузить список, попробуй ещё раз.", nil)
		return
	}
	t.send(ctx, update.Message.Chat.ID, text, markup)
}

func (t *Bot) adminsScreen(ctx context.Context) (string, models.ReplyMarkup, error) {
	admins, err := t.store.ListUsers(ctx, storage.RoleAdmin, 100)
	if err != nil {
		return "", nil, err
	}
	sales, err := t.store.ListUsers(ctx, storage.RoleSales, 100)
	if err != nil {
		return "", nil, err
	}
	primary, err := t.store.PrimaryAdmin(ctx)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return "", nil, err
	}
	candidates, err := t.store.ListAdminCandidates(ctx, t.superadminID, adminCandidatesLimit)
	if err != nil {
		return "", nil, err
	}

	var sb strings.Builder
	var rows [][]models.InlineKeyboardButton
	button := func(text, action string, id int64) models.InlineKeyboardButton {
		return models.InlineKeyboardButton{
			Text:         text,
			CallbackData: fmt.Sprintf("%s%s:%d", cbAdminPrefix, action, id),
		}
	}

	sb.WriteString("🛡 <b>Админы</b>\n")
	if len(admins) == 0 {
		sb.WriteString("пока нет\n")
	}
	for _, u := range admins {
		if u.TgID == primary.TgID {
			sb.WriteString("• " + displayName(u) + " — 📌 основной, его тегает бот\n")
			rows = append(rows, []models.InlineKeyboardButton{button("❌ Снять "+plainName(u), cbAdminAsk, u.TgID)})
			continue
		}
		sb.WriteString("• " + displayName(u) + "\n")
		rows = append(rows, []models.InlineKeyboardButton{
			button("📌 Основной", cbAdminMain, u.TgID),
			button("❌ Снять "+plainName(u), cbAdminAsk, u.TgID),
		})
	}

	sb.WriteString("\n💼 <b>Sales</b>\n")
	if len(sales) == 0 {
		sb.WriteString("пока нет\n")
	}
	for _, u := range sales {
		sb.WriteString("• " + displayName(u) + "\n")
		rows = append(rows, []models.InlineKeyboardButton{button("❌ Снять sales "+plainName(u), cbAdminAsk, u.TgID)})
	}

	if len(candidates) > 0 {
		sb.WriteString("\nПисали боту:\n")
	}
	for _, u := range candidates {
		sb.WriteString("• " + displayName(u) + "\n")
		rows = append(rows, []models.InlineKeyboardButton{
			button("⭐ Админ "+plainName(u), cbAdminAdd, u.TgID),
			button("💼 Sales "+plainName(u), cbAdminSales, u.TgID),
		})
	}
	if len(candidates) == 0 {
		sb.WriteString("\nЧтобы дать человеку роль, пусть он напишет боту /start.")
	}

	var markup models.ReplyMarkup
	if len(rows) > 0 {
		markup = &models.InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	return sb.String(), markup, nil
}

func (t *Bot) onAdminCallback(ctx context.Context, b *bot.Bot, update *models.Update) {
	cq := update.CallbackQuery
	answer := func(text string) {
		_, err := b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID, Text: text})
		if err != nil {
			t.log.Error("answer callback", "err", err)
		}
	}

	if !isSuperadmin(t.roleOf(ctx, cq.From.ID)) {
		answer("Нет доступа")
		return
	}

	action, rawID, _ := strings.Cut(strings.TrimPrefix(cq.Data, cbAdminPrefix), ":")
	if action == cbAdminBack {
		answer("")
		t.refreshAdminsScreen(ctx, cq.Message)
		return
	}
	targetID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || targetID == t.superadminID {
		answer("Некорректная кнопка")
		return
	}

	if action == cbAdminAsk || action == cbAdminLegacyDel {
		answer("")
		t.askRemoveRole(ctx, cq.Message, targetID)
		return
	}

	if action == cbAdminMain {
		if err := t.store.SetPrimaryAdmin(ctx, targetID); err != nil {
			t.log.Error("set primary admin", "tg_id", targetID, "err", err)
			answer("Не получилось: это не админ?")
			return
		}
		t.log.Info("primary admin changed", "tg_id", targetID, "by", cq.From.ID)
		answer("Теперь это основной админ")
		t.refreshAdminsScreen(ctx, cq.Message)
		return
	}

	var role storage.Role
	switch action {
	case cbAdminAdd:
		role = storage.RoleAdmin
	case cbAdminSales:
		role = storage.RoleSales
	case cbAdminDel:
		role = storage.RoleMember
	default:
		answer("Некорректная кнопка")
		return
	}

	if err := t.store.SetRole(ctx, targetID, role); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			answer("Пользователь не найден")
		} else {
			t.log.Error("set role", "tg_id", targetID, "err", err)
			answer("Ошибка, попробуй ещё раз")
		}
		return
	}
	t.log.Info("role changed", "tg_id", targetID, "role", role, "by", cq.From.ID)

	// The user's private chat ID equals their user ID.
	t.syncMenu(ctx, targetID, role)
	switch role {
	case storage.RoleAdmin:
		answer("Назначен админом")
		t.send(ctx, targetID, "Тебя назначили админом бота. Команды — в меню слева от поля ввода.", nil)
	case storage.RoleSales:
		answer("Назначен sales")
		t.send(ctx, targetID, "Тебе назначена роль sales 💼\n"+
			"По будням в 18:00 я буду напоминать отправить отчёт за день.", nil)
	default:
		answer("Роль снята")
		t.send(ctx, targetID, "Твоя роль в боте снята.", nil)
	}

	t.refreshAdminsScreen(ctx, cq.Message)
}

// refreshAdminsScreen redraws the message the button was pressed on.
func (t *Bot) refreshAdminsScreen(ctx context.Context, m models.MaybeInaccessibleMessage) {
	if m.Message == nil {
		return // too old to edit
	}
	text, markup, err := t.adminsScreen(ctx)
	if err != nil {
		t.log.Error("admins screen", "err", err)
		return
	}
	_, err = t.api.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:      m.Message.Chat.ID,
		MessageID:   m.Message.ID,
		Text:        text,
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: markup,
	})
	if err != nil {
		t.log.Error("edit admins screen", "err", err)
	}
}

// notifyNewUser tells the superadmin that someone without access wrote to the bot.
func (t *Bot) notifyNewUser(ctx context.Context, u storage.User) {
	if t.superadminID == 0 {
		return
	}
	text := fmt.Sprintf("👤 Боту написал новый человек: %s\nID: <code>%d</code>", displayName(u), u.TgID)
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: "⭐ Сделать админом", CallbackData: fmt.Sprintf("%s%s:%d", cbAdminPrefix, cbAdminAdd, u.TgID)},
		{Text: "💼 Сделать sales", CallbackData: fmt.Sprintf("%s%s:%d", cbAdminPrefix, cbAdminSales, u.TgID)},
	}}}
	t.send(ctx, t.superadminID, text, markup)
}

func (t *Bot) onStatus(ctx context.Context, _ *bot.Bot, update *models.Update) {
	db := "✅ доступна"
	if err := t.store.Ping(ctx); err != nil {
		db = "❌ " + err.Error()
	}
	text := fmt.Sprintf("ℹ️ <b>Статус</b>\n\n"+
		"Версия: <code>%s</code>\n"+
		"Работает: %s\n"+
		"База: %s\n",
		version.Version, time.Since(t.startedAt).Round(time.Second), db)
	text += t.yougileStatus(ctx) + "\n"
	if s := t.testModeStatus(ctx); s != "" {
		text += "\n" + s
	}

	st, err := t.store.Stats(ctx, t.superadminID)
	if err != nil {
		t.log.Error("load stats", "err", err)
		t.send(ctx, update.Message.Chat.ID, text+"\nСтатистику загрузить не удалось.", nil)
		return
	}

	have := func(n int) string {
		if n > 0 {
			return "✅ есть"
		}
		return "❌ нет"
	}
	group := "❌ бот пока не добавлен ни в один чат"
	if st.ActiveChats > 0 {
		group = fmt.Sprintf("всего %d\n   📎 материалы: %s · 🔔 выкладка: %s · 💼 продажи: %s",
			st.ActiveChats, have(st.MaterialChats), have(st.PublishChats), have(st.SalesChats))
		if st.Unassigned > 0 {
			group += fmt.Sprintf("\n   без назначения: %d — см. /chats", st.Unassigned)
		}
	}

	text += fmt.Sprintf("\n👥 Пользователей: %d\n"+
		"   админов: %d · sales: %d · обычных: %d\n"+
		"   замечены в групповом чате: %d\n"+
		"\n💬 Групповые чаты: %s",
		st.Users, st.Admins, st.Sales, st.Regular, st.InGroups, group)
	t.send(ctx, update.Message.Chat.ID, text, nil)
}

// plainName is displayName for button labels, which are not HTML.
func plainName(u storage.User) string {
	if u.Username != "" {
		return "@" + u.Username
	}
	return u.FirstName
}

// askRemoveRole replaces the roles screen with a confirmation, so a role is
// not removed by an accidental tap.
func (t *Bot) askRemoveRole(ctx context.Context, m models.MaybeInaccessibleMessage, targetID int64) {
	if m.Message == nil {
		return
	}
	u, err := t.store.GetUser(ctx, targetID)
	if err != nil {
		t.log.Error("load user", "tg_id", targetID, "err", err)
		return
	}
	role := "админов"
	if u.Role == storage.RoleSales {
		role = "sales"
	}
	text := fmt.Sprintf("Точно снять %s с %s?\nЕму придёт сообщение, меню и напоминания пропадут.", displayName(u), role)
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: "✅ Да, снять", CallbackData: fmt.Sprintf("%s%s:%d", cbAdminPrefix, cbAdminDel, targetID)},
		{Text: "↩️ Отмена", CallbackData: cbAdminPrefix + cbAdminBack + ":0"},
	}}}
	_, err = t.api.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID: m.Message.Chat.ID, MessageID: m.Message.ID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: markup,
	})
	if err != nil {
		t.log.Error("edit admins screen", "err", err)
	}
}
