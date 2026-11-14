package telegram

import (
	"context"
	"fmt"
	"html"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"remindbot/internal/storage"
	"remindbot/internal/version"
)

func (t *Bot) onStart(ctx context.Context, _ *bot.Bot, update *models.Update) {
	msg := update.Message
	if msg.From == nil || msg.Chat.Type != models.ChatTypePrivate {
		return
	}
	u := storage.User{
		TgID:      msg.From.ID,
		Username:  msg.From.Username,
		FirstName: msg.From.FirstName,
	}
	created, err := t.store.TouchUser(ctx, u)
	if err == nil {
		err = t.store.MarkStarted(ctx, u.TgID)
	}
	if err != nil {
		t.log.Error("save user", "tg_id", u.TgID, "err", err)
	}

	role := t.roleOf(ctx, u.TgID)
	if !isAdmin(role) {
		t.send(ctx, msg.Chat.ID,
			"Это приватный бот команды. Если тебе нужен доступ, напиши администратору.",
			removeKeyboard)
		if created {
			t.notifyNewUser(ctx, u)
		}
		return
	}

	// Re-syncing here lets anyone fix a stale menu by sending /start.
	t.syncMenu(ctx, u.TgID, role)
	text := fmt.Sprintf("Привет, %s! Команды — в меню слева от поля ввода.", html.EscapeString(u.FirstName))
	t.send(ctx, msg.Chat.ID, text, removeKeyboard)
}

func (t *Bot) onWhoami(ctx context.Context, _ *bot.Bot, update *models.Update) {
	msg := update.Message
	if msg.From == nil {
		return
	}
	text := fmt.Sprintf("Твой Telegram ID: <code>%d</code>", msg.From.ID)
	if msg.Chat.Type != models.ChatTypePrivate {
		text += fmt.Sprintf("\nID этого чата: <code>%d</code>", msg.Chat.ID)
	}
	text += fmt.Sprintf("\nВерсия бота: <code>%s</code>", html.EscapeString(version.Version))
	t.reply(ctx, msg, text)
}

// The month report is not done yet; until then the command says so.
const reportNotReady = "\n\nОтчёт пока не готов — сделаем следующим шагом."

func (t *Bot) onToday(ctx context.Context, _ *bot.Bot, update *models.Update) {
	text := "📅 <b>Сегодня, " + formatDay(time.Now().In(t.tz)) + "</b>"
	tasks, err := t.tasksTodayText(ctx)
	if err != nil {
		t.log.Error("tasks today", "err", err)
		tasks = "📌 Публикации загрузить не удалось."
	}
	text += "\n\n" + tasks
	if t.features.SalesPoll {
		sales, err := t.salesTodayText(ctx)
		if err != nil {
			t.log.Error("sales today", "err", err)
			sales = "💼 Отчёты продажников загрузить не удалось."
		}
		if sales != "" {
			text += "\n\n" + sales
		}
	}
	t.reply(ctx, update.Message, text)
}

func (t *Bot) onWeek(ctx context.Context, _ *bot.Bot, update *models.Update) {
	text, err := t.weekReport(ctx)
	if err != nil {
		t.log.Error("week report", "err", err)
		text = "📊 Отчёт за неделю загрузить не удалось."
	}
	t.reply(ctx, update.Message, text)
}

func (t *Bot) onMonth(ctx context.Context, _ *bot.Bot, update *models.Update) {
	t.reply(ctx, update.Message, "🗓 <b>Месяц</b>"+reportNotReady)
}

func (t *Bot) onUnhandled(_ context.Context, _ *bot.Bot, update *models.Update) {
	t.log.Debug("unhandled update", "update_id", update.ID)
}
