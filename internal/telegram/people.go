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

// People: who is who. The superadmin sets each person's role and links them
// to their YouGile account, so reminders about their tasks tag them.
//
// Callback data: "ppl:<action>:<tg_id>[:<arg>]".
const (
	cbPeoplePrefix = "ppl:"
	cbPeopleList   = "list" // back to /people
	cbPeopleOpen   = "open" // a person's card
	cbPeopleRole   = "role" // arg: admin | sales | member
	cbPeopleRmOK   = "rmok" // removing the role, confirmed
	cbPeopleLink   = "yg"   // arg: YouGile user ID, or cbPeopleNoYG
	cbPeopleLinkOK = "ygok" // arg: YouGile user ID; taking it from another person, confirmed
	cbPeopleNoYG   = "-"
	peopleLimit    = 60
	assigneesLimit = 40
)

// onPeople shows /people: the superadmin can change roles and links, admins
// only see who is who.
func (t *Bot) onPeople(ctx context.Context, _ *bot.Bot, update *models.Update) {
	editable := isSuperadmin(t.roleOf(ctx, update.Message.From.ID))
	text, markup, err := t.peopleScreen(ctx, editable)
	if err != nil {
		t.log.Error("people screen", "err", err)
		t.send(ctx, update.Message.Chat.ID, "Не удалось загрузить людей, попробуй ещё раз.", nil)
		return
	}
	t.send(ctx, update.Message.Chat.ID, text, markup)
}

var roleIcons = map[storage.Role]string{
	storage.RoleSuperadmin: "👑",
	storage.RoleAdmin:      "⭐",
	storage.RoleSales:      "💼",
	storage.RoleMember:     "👤",
}

var roleNames = map[storage.Role]string{
	storage.RoleSuperadmin: "суперадмин",
	storage.RoleAdmin:      "админ",
	storage.RoleSales:      "продажник",
	storage.RoleMember:     "без роли",
}

// peopleScreen lists everyone with their role and YouGile link, and the
// YouGile assignees nobody is linked to.
func (t *Bot) peopleScreen(ctx context.Context, editable bool) (string, models.ReplyMarkup, error) {
	people, err := t.store.ListPeople(ctx, t.superadminID, peopleLimit)
	if err != nil {
		return "", nil, err
	}
	assignees, err := t.store.Assignees(ctx)
	if err != nil {
		return "", nil, err
	}
	linked := map[int64]storage.YouGilePerson{}
	var unlinked []string
	for _, a := range assignees {
		if a.User.TgID != 0 {
			linked[a.User.TgID] = a
		} else {
			unlinked = append(unlinked, html.EscapeString(ygLabel(a)))
		}
	}

	var sb strings.Builder
	sb.WriteString("👥 <b>Люди</b>\n<i>роль · кто в YouGile</i>\n")
	if len(people) == 0 {
		sb.WriteString("\nПока никого. Добавь бота в чат — он поздоровается и попросит всех нажать «Начать».")
	}
	var rows [][]models.InlineKeyboardButton
	var row []models.InlineKeyboardButton
	for _, u := range people {
		if u.TgID == t.superadminID {
			u.Role = storage.RoleSuperadmin // set in the config, not stored
		}
		yg := "—"
		if a, ok := linked[u.TgID]; ok {
			yg = html.EscapeString(ygLabel(a))
		}
		fmt.Fprintf(&sb, "\n%s %s · %s", roleIcons[roleOrMember(u)], displayName(u), yg)
		row = append(row, models.InlineKeyboardButton{
			Text: "✏️ " + plainName(u), CallbackData: peopleData(cbPeopleOpen, u.TgID, ""),
		})
		if len(row) == 2 {
			rows, row = append(rows, row), nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	if len(unlinked) > 0 {
		sb.WriteString("\n\n🔗 <b>В YouGile, но не связаны с Telegram:</b>\n" + strings.Join(unlinked, ", ") +
			"\nБот пишет им имя без тега.")
		if editable {
			sb.WriteString(" Связать — в карточке человека (✏️).")
		}
	}
	sb.WriteString("\n\nЛюди появляются здесь, когда пишут боту или нажимают «Начать» в приветствии в чате.")
	if !editable {
		sb.WriteString("\nМенять роли и связки может суперадмин.")
	}
	var markup models.ReplyMarkup
	if editable && len(rows) > 0 {
		markup = &models.InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	return sb.String(), markup, nil
}

func roleOrMember(u storage.User) storage.Role {
	if u.Role == "" {
		return storage.RoleMember
	}
	return u.Role
}

// personCard shows one person: role buttons and whom they are in YouGile.
func (t *Bot) personCard(ctx context.Context, tgID int64) (string, models.ReplyMarkup, error) {
	u, err := t.store.GetUser(ctx, tgID)
	if err != nil {
		return "", nil, err
	}
	if tgID == t.superadminID {
		u.Role = storage.RoleSuperadmin
	}
	started, err := t.store.HasStarted(ctx, tgID)
	if err != nil {
		return "", nil, err
	}
	cur, hasYG, err := t.store.LinkedYouGile(ctx, tgID)
	if err != nil {
		return "", nil, err
	}
	assignees, err := t.store.Assignees(ctx)
	if err != nil {
		return "", nil, err
	}

	yg := "не связан — бот пишет имя без тега"
	if hasYG {
		yg = html.EscapeString(ygLabel(cur)) + " — бот отмечает " + displayName(u)
	}
	name := displayName(u)
	if u.Username != "" && u.FirstName != "" {
		name += " (" + html.EscapeString(u.FirstName) + ")"
	}
	text := fmt.Sprintf("👤 <b>%s</b>\nРоль: %s %s\nYouGile: %s\n\nКто это в YouGile?",
		name, roleIcons[roleOrMember(u)], roleNames[roleOrMember(u)], yg)

	var rows [][]models.InlineKeyboardButton
	if u.Role != storage.RoleSuperadmin && !started {
		text = strings.Replace(text, "\n\nКто это в YouGile?",
			"\n<i>Ещё не нажал «Начать» в личке с ботом — роль можно дать после этого: бот должен иметь возможность ему написать.</i>"+
				"\n\nКто это в YouGile?", 1)
	}
	if u.Role != storage.RoleSuperadmin && started {
		mark := func(r storage.Role, label string) models.InlineKeyboardButton {
			if roleOrMember(u) == r {
				label = "✓ " + label
			}
			return models.InlineKeyboardButton{Text: label, CallbackData: peopleData(cbPeopleRole, tgID, string(r))}
		}
		rows = append(rows, []models.InlineKeyboardButton{
			mark(storage.RoleAdmin, "⭐ Админ"), mark(storage.RoleSales, "💼 Продажник"), mark(storage.RoleMember, "👤 Без роли"),
		})
	}
	var row []models.InlineKeyboardButton
	for i, a := range assignees {
		if i == assigneesLimit {
			break
		}
		label := ygLabel(a)
		switch {
		case hasYG && a.ID == cur.ID:
			label = "✓ " + label
		case a.User.TgID != 0:
			label += " · " + plainName(a.User)
		}
		row = append(row, models.InlineKeyboardButton{Text: label, CallbackData: peopleData(cbPeopleLink, tgID, a.ID)})
		if len(row) == 2 {
			rows, row = append(rows, row), nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	if len(assignees) == 0 {
		text += "\n<i>Исполнителей из YouGile пока нет: они появятся после сверки с YouGile.</i>"
	}
	noYG := "🚫 Нет в YouGile"
	if !hasYG {
		noYG = "✓ " + noYG
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{{Text: noYG, CallbackData: peopleData(cbPeopleLink, tgID, cbPeopleNoYG)}},
		[]models.InlineKeyboardButton{{Text: "← Все люди", CallbackData: peopleData(cbPeopleList, 0, "")}},
	)
	return text, &models.InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

// ygLabel names a YouGile person for the superadmin's screens. Unlike the
// team's messages it may show an e-mail: YouGile puts it in the name when no
// name is set, and the superadmin needs it to tell people apart.
func ygLabel(p storage.YouGilePerson) string {
	if n := strings.TrimSpace(p.Name); n != "" {
		return n
	}
	return "без имени"
}

func peopleData(action string, tgID int64, arg string) string {
	d := cbPeoplePrefix + action + ":" + strconv.FormatInt(tgID, 10)
	if arg != "" {
		d += ":" + arg
	}
	return d
}

func (t *Bot) onPeopleCallback(ctx context.Context, b *bot.Bot, update *models.Update) {
	cq := update.CallbackQuery
	answer := func(text string) {
		if _, err := b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID, Text: text}); err != nil {
			t.log.Error("answer callback", "err", err)
		}
	}
	if !isSuperadmin(t.roleOf(ctx, cq.From.ID)) {
		answer("Нет доступа")
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(cq.Data, cbPeoplePrefix), ":", 3)
	if len(parts) < 2 {
		answer("Некорректная кнопка")
		return
	}
	action, arg := parts[0], ""
	if len(parts) == 3 {
		arg = parts[2]
	}
	tgID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		answer("Некорректная кнопка")
		return
	}

	switch action {
	case cbPeopleList:
		answer("")
		text, markup, err := t.peopleScreen(ctx, true)
		if err != nil {
			t.log.Error("people screen", "err", err)
			return
		}
		t.editScreen(ctx, cq.Message, text, markup)
		return
	case cbPeopleOpen:
		answer("")
	case cbPeopleRole, cbPeopleRmOK:
		role := storage.Role(arg)
		if action == cbPeopleRmOK {
			role = storage.RoleMember
		}
		if tgID == t.superadminID || (role != storage.RoleAdmin && role != storage.RoleSales && role != storage.RoleMember) {
			answer("Некорректная кнопка")
			return
		}
		u, err := t.store.GetUser(ctx, tgID)
		if err != nil {
			t.log.Error("load user", "tg_id", tgID, "err", err)
			answer("Пользователь не найден")
			return
		}
		if roleOrMember(u) == role {
			answer("Роль уже такая")
			return
		}
		if started, err := t.store.HasStarted(ctx, tgID); err != nil || !started {
			answer("Сначала человек должен нажать «Начать» в личке с ботом")
			return
		}
		// Taking a role away asks first, as in /admins: an accidental tap
		// could leave the team without the admin the bot tags.
		if role == storage.RoleMember && action != cbPeopleRmOK {
			answer("")
			t.editScreen(ctx, cq.Message, fmt.Sprintf("Снять роль «%s» у %s?", roleNames[roleOrMember(u)], displayName(u)),
				&models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
					{Text: "Да, снять", CallbackData: peopleData(cbPeopleRmOK, tgID, "")},
					{Text: "Отмена", CallbackData: peopleData(cbPeopleOpen, tgID, "")},
				}}})
			return
		}
		answer(t.applyRole(ctx, tgID, role, cq.From.ID))
	case cbPeopleLink, cbPeopleLinkOK:
		ygID := arg
		if ygID == cbPeopleNoYG {
			ygID = ""
		}
		// A YouGile person linked to someone else is taken over only after
		// a confirmation.
		if ygID != "" && action == cbPeopleLink {
			if other, ok, err := t.linkedElsewhere(ctx, ygID, tgID); err != nil {
				t.log.Error("load assignees", "err", err)
				answer("Ошибка, попробуй ещё раз")
				return
			} else if ok {
				answer("")
				t.editScreen(ctx, cq.Message, fmt.Sprintf(
					"%s в YouGile сейчас связан(а) с %s. Перепривязать? Тогда бот перестанет отмечать %s по этим задачам.",
					html.EscapeString(ygLabel(other)), displayName(other.User), displayName(other.User)),
					&models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
						{Text: "Да, перепривязать", CallbackData: peopleData(cbPeopleLinkOK, tgID, ygID)},
						{Text: "Отмена", CallbackData: peopleData(cbPeopleOpen, tgID, "")},
					}}})
				return
			}
		}
		if err := t.store.LinkYouGile(ctx, tgID, ygID); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				answer("Этого человека нет в YouGile")
			} else {
				t.log.Error("link yougile", "tg_id", tgID, "err", err)
				answer("Ошибка, попробуй ещё раз")
			}
			return
		}
		t.log.Info("yougile link changed", "tg_id", tgID, "yougile_id", ygID, "by", cq.From.ID)
		if ygID == "" {
			answer("Связь с YouGile снята")
		} else {
			answer("Связано: теперь бот будет отмечать этого человека")
		}
	default:
		answer("Некорректная кнопка")
		return
	}

	text, markup, err := t.personCard(ctx, tgID)
	if err != nil {
		t.log.Error("person card", "tg_id", tgID, "err", err)
		return
	}
	t.editScreen(ctx, cq.Message, text, markup)
}

// linkedElsewhere returns the YouGile person ygID if it is linked to a
// Telegram user other than tgID.
func (t *Bot) linkedElsewhere(ctx context.Context, ygID string, tgID int64) (storage.YouGilePerson, bool, error) {
	assignees, err := t.store.Assignees(ctx)
	if err != nil {
		return storage.YouGilePerson{}, false, err
	}
	for _, a := range assignees {
		if a.ID == ygID && a.User.TgID != 0 && a.User.TgID != tgID {
			return a, true, nil
		}
	}
	return storage.YouGilePerson{}, false, nil
}

// editScreen redraws the message a button was pressed on.
func (t *Bot) editScreen(ctx context.Context, m models.MaybeInaccessibleMessage, text string, markup models.ReplyMarkup) {
	if m.Message == nil {
		return // too old to edit
	}
	if _, err := t.api.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID: m.Message.Chat.ID, MessageID: m.Message.ID, Text: text,
		ParseMode: models.ParseModeHTML, ReplyMarkup: markup,
	}); err != nil {
		t.log.Error("edit screen", "err", err)
	}
}

// notifyNewPerson tells the superadmin that someone pressed "Start" for the
// first time, with their card to set the role and the YouGile link.
func (t *Bot) notifyNewPerson(ctx context.Context, u storage.User) {
	if t.superadminID == 0 || u.TgID == t.superadminID {
		return
	}
	text, markup, err := t.personCard(ctx, u.TgID)
	if err != nil {
		t.log.Error("person card", "tg_id", u.TgID, "err", err)
		text, markup = fmt.Sprintf("👤 %s", displayName(u)), nil
	}
	t.send(ctx, t.superadminID, "🆕 <b>Новый человек в боте</b>\n\n"+text, markup)
}

// Greeting in a group chat: what the bot will post there, and a button that
// opens a private chat with the bot, so people start it in one tap.
var greetAbout = map[storage.ChatPurpose]string{
	storage.PurposeMaterial: "буду просить здесь материал на согласование — за сутки до поста или рилс",
	storage.PurposePublish:  "буду напоминать здесь о выкладке — за час и за полчаса до публикации",
	storage.PurposeSales:    "буду напоминать здесь заполнить отчёт в Яндекс Документах — по понедельникам, средам и пятницам",
	storage.PurposeContent:  "буду просить здесь материал на согласование и напоминать о выкладке",
}

func (t *Bot) greetChat(ctx context.Context, chatID int64, purpose storage.ChatPurpose) {
	about, ok := greetAbout[purpose]
	if !ok {
		return
	}
	text := "👋 Всем привет! Я " + html.EscapeString(t.botName()) + ", " + about + ".\n\n" +
		"Нажмите, пожалуйста, <b>«Начать»</b> — откроется чат со мной, там тоже нажмите «Начать». " +
		"Это нужно один раз, чтобы я мог отмечать вас в сообщениях. Писать мне ничего не нужно."
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: "👋 Начать", URL: "https://t.me/" + t.username + "?start=hi"},
	}}}
	// A greeting is sent on the superadmin's or an admin's action, so it goes
	// to the chat even in test mode.
	if m := t.send(ctx, chatID, text, markup); m != nil {
		t.log.Info("chat greeted", "chat_id", chatID, "purpose", purpose)
	}
}

func (t *Bot) botName() string {
	if t.firstName != "" {
		return t.firstName
	}
	return "@" + t.username
}

// onHello reposts the greeting in a group, for people who came later. Only
// the superadmin and admins can ask for it.
func (t *Bot) onHello(ctx context.Context, _ *bot.Bot, update *models.Update) {
	msg := update.Message
	if msg.From == nil || msg.Chat.Type == models.ChatTypePrivate || !isAdmin(t.roleOf(ctx, msg.From.ID)) {
		return
	}
	purpose, err := t.store.ChatPurposeOf(ctx, msg.Chat.ID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		t.log.Error("chat purpose", "chat_id", msg.Chat.ID, "err", err)
		return
	}
	if purpose == storage.PurposeNone {
		t.reply(ctx, msg, "У этого чата пока нет роли. Суперадмин назначает её в /chats, тогда я поздороваюсь.")
		return
	}
	t.greetChat(ctx, msg.Chat.ID, purpose)
}
