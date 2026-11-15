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

// Callback data for the admins screen: "adm:<action>:<tg_id>".
const (
	cbAdminPrefix = "adm:"
	cbAdminAdd    = "add"  // make admin
	cbAdminMain   = "main" // make the primary admin
	cbAdminAsk    = "ask"  // ask to confirm removing the role
	cbAdminDel    = "rm"   // remove the role (confirmed)
	// Old screens offered "make sales"; sales are now set in /people.
	cbAdminLegacySales = "sales"
	// Old screens sent "del" for an immediate removal; it now asks too.
	cbAdminLegacyDel = "del"
	cbAdminBack      = "back" // cancel: back to the admins screen
)

// salesInPeople answers a stale /admins button about a sales person: the
// admins screen is for admins only.
const salesInPeople = "Продажников назначают и снимают в /people"

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

	if len(candidates) > 0 {
		sb.WriteString("\nПисали боту:\n")
	}
	for _, u := range candidates {
		sb.WriteString("• " + displayName(u) + "\n")
		rows = append(rows, []models.InlineKeyboardButton{button("⭐ Админ "+plainName(u), cbAdminAdd, u.TgID)})
	}
	if len(candidates) == 0 {
		sb.WriteString("\nЧтобы сделать человека админом, пусть он напишет боту /start.")
	}
	sb.WriteString("\n\nПродажники и все остальные — в /people.")

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
		if refusal := t.askRemoveRole(ctx, cq.Message, targetID); refusal != "" {
			// A stale screen: show the current one.
			answer(refusal)
			t.refreshAdminsScreen(ctx, cq.Message)
			return
		}
		answer("")
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
	case cbAdminLegacySales:
		answer(salesInPeople)
		t.refreshAdminsScreen(ctx, cq.Message)
		return
	case cbAdminDel:
		role = storage.RoleMember
	default:
		answer("Некорректная кнопка")
		return
	}

	// Redrawn after any answer, a refusal too: a stale button goes away.
	answer(t.applyRole(ctx, targetID, role, cq.From.ID))
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

// roleUnchanged is roleTarget's refusal when the person already has the role.
const roleUnchanged = "Роль уже такая"

// roleTarget loads the person whose role is to become role and checks the
// rules for both /admins and /people: a role is given only to someone who
// has pressed Start (the bot must be able to write to them); taking one away
// needs nothing; the current role is not set again. refusal is the text for
// the button answer, or "" if the change may go ahead.
func (t *Bot) roleTarget(ctx context.Context, targetID int64, role storage.Role) (u storage.User, started bool, refusal string) {
	u, started, err := t.store.GetPerson(ctx, targetID)
	if errors.Is(err, storage.ErrNotFound) {
		return u, false, "Пользователь не найден"
	}
	if err != nil {
		t.log.Error("load user", "tg_id", targetID, "err", err)
		return u, false, "Ошибка, попробуй ещё раз"
	}
	if roleOrMember(u) == role {
		return u, started, roleUnchanged
	}
	if role != storage.RoleMember && !started {
		return u, started, "Сначала человек должен нажать «Начать» в личке с ботом"
	}
	return u, started, ""
}

// applyRole sets a person's role, updates their menu and tells them, and
// returns the text for the button answer. The rules are in roleTarget;
// started is read right before the write, so the notice matches the
// person's state.
func (t *Bot) applyRole(ctx context.Context, targetID int64, role storage.Role, by int64) string {
	_, started, refusal := t.roleTarget(ctx, targetID, role)
	if refusal == roleUnchanged {
		// The role may have been changed elsewhere (another screen, the
		// database) without the menu: bring the menu in line anyway.
		t.syncMenu(ctx, targetID, role)
	}
	if refusal != "" {
		return refusal
	}
	if err := t.store.SetRole(ctx, targetID, role); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "Пользователь не найден"
		}
		t.log.Error("set role", "tg_id", targetID, "err", err)
		return "Ошибка, попробуй ещё раз"
	}
	t.log.Info("role changed", "tg_id", targetID, "role", role, "by", by, "started", started)

	// The user's private chat ID equals their user ID. The menu is synced
	// even for someone who never pressed Start: removing one cannot hurt,
	// and syncMenu only logs that the person is unreachable.
	t.syncMenu(ctx, targetID, role)
	var notice, done string
	switch role {
	case storage.RoleAdmin:
		notice, done = "Тебя назначили админом бота. Команды — в меню слева от поля ввода.", "Назначен админом"
	case storage.RoleSales:
		notice, done = "Тебе назначена роль продажника 💼\n"+
			"По понедельникам, средам и пятницам в 16:00 я буду напоминать заполнить отчёт в Яндекс Документах.",
			"Назначен продажником"
	case storage.RoleTelegram:
		notice, done = "Тебе назначена роль «Телеграм» ✈️\n"+
			"В дни, когда есть посты в телеграме, за час до первого я отмечу тебя в чате выкладки и пришлю их список.",
			"Назначен на Телеграм"
	default:
		notice, done = "Твоя роль в боте снята.", "Роль снята"
	}
	// Not writing at all to someone who never pressed Start gives a clearer
	// answer than a failed send.
	if !started {
		return done + " (человек не уведомлён: не нажимал «Начать»)"
	}
	// Someone who pressed Start may have blocked the bot since.
	if t.sendToPerson(ctx, targetID, notice, nil) == nil {
		return done + " (человек не уведомлён: сообщение не дошло)"
	}
	return done
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
		group = fmt.Sprintf("всего %d\n   📎 материалы: %s · 🔔 выкладка: %s · 💼 продажи: %s · ✈️ телеграм: %s",
			st.ActiveChats, have(st.MaterialChats), have(st.PublishChats), have(st.SalesChats), have(st.TelegramChats))
		if st.Unassigned > 0 {
			group += fmt.Sprintf("\n   без назначения: %d — см. /chats", st.Unassigned)
		}
	}

	text += fmt.Sprintf("\n👥 Пользователей: %d\n"+
		"   админов: %d · sales: %d · телеграм: %d · обычных: %d\n"+
		"   замечены в групповом чате: %d\n"+
		"\n💬 Групповые чаты: %s",
		st.Users, st.Admins, st.Sales, st.Telegram, st.Regular, st.InGroups, group)
	t.send(ctx, update.Message.Chat.ID, text, nil)
}

// plainName is displayName for button labels, which are not HTML.
func plainName(u storage.User) string {
	if u.Username != "" {
		return "@" + u.Username
	}
	return u.FirstName
}

// askRemoveRole replaces the admins screen with a confirmation, so a role is
// not removed by an accidental tap. It returns roleTarget's refusal (for
// one, the person has no role any more), and then asks nothing.
func (t *Bot) askRemoveRole(ctx context.Context, m models.MaybeInaccessibleMessage, targetID int64) string {
	u, _, refusal := t.roleTarget(ctx, targetID, storage.RoleMember)
	if refusal != "" {
		return refusal
	}
	if u.Role != storage.RoleAdmin {
		return "Это не админ: его роль меняют в /people" // a stale screen
	}
	if m.Message == nil {
		return ""
	}
	text := fmt.Sprintf("Точно снять %s с админов?\nЕму придёт сообщение, меню пропадёт.", displayName(u))
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: "✅ Да, снять", CallbackData: fmt.Sprintf("%s%s:%d", cbAdminPrefix, cbAdminDel, targetID)},
		{Text: "↩️ Отмена", CallbackData: cbAdminPrefix + cbAdminBack + ":0"},
	}}}
	_, err := t.api.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID: m.Message.Chat.ID, MessageID: m.Message.ID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: markup,
	})
	if err != nil {
		t.log.Error("edit admins screen", "err", err)
	}
	return ""
}
