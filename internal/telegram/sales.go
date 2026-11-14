package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/robfig/cron/v3"

	"remindbot/internal/storage"
)

// Daily sales report check, Mon–Fri in the bot's time zone. Messages go to
// the sales group chat with a tag of the person; without such a chat, to DMs.
//
//	18:00  ask every sales user to send the day's report to the admins
//	18:20  "sent it?" [Yes] [No] — attempt 1
//	18:40  same for those without "yes" — attempt 2
//	20:40  after a grace period, close the day: no answer → "no_answer";
//	       summary to admins. Until then "yes" is still accepted.
const (
	salesCron     = "0 18 * * 1-5"
	salesStep     = 20 * time.Minute
	salesAttempts = 2
	salesGrace    = 2 * time.Hour
	// If the bot was down at 18:00, the day still starts when it comes back,
	// but not later than this hour.
	salesLatestStartHour = 21
)

// Callback data: "sr:<date>:<owner tg_id>:y|n".
const cbSalesPrefix = "sr:"

// salesDayArgs is the periodic job that starts the day.
type salesDayArgs struct{}

func (salesDayArgs) Kind() string { return "sales_day" }

// salesStepArgs asks attempt Step; Step salesAttempts+1 closes the day.
type salesStepArgs struct {
	Date string `json:"date"`
	Step int    `json:"step"`
}

func (salesStepArgs) Kind() string { return "sales_step" }

func (t *Bot) salesJobs(workers *river.Workers) ([]*river.PeriodicJob, error) {
	river.AddWorker(workers, river.WorkFunc(t.workSalesDay))
	river.AddWorker(workers, river.WorkFunc(t.workSalesStep))

	schedule, err := cron.ParseStandard("CRON_TZ=" + t.tz.String() + " " + salesCron)
	if err != nil {
		return nil, fmt.Errorf("sales schedule: %w", err)
	}
	return []*river.PeriodicJob{
		river.NewPeriodicJob(schedule,
			func() (river.JobArgs, *river.InsertOpts) { return salesDayArgs{}, nil },
			// RunOnStart catches up if the bot was down at 18:00.
			&river.PeriodicJobOpts{ID: "sales_day", RunOnStart: true}),
	}, nil
}

func (t *Bot) workSalesDay(ctx context.Context, _ *river.Job[salesDayArgs]) error {
	now := time.Now().In(t.tz)
	if !salesWorkday(now) || now.Hour() < 18 || now.Hour() >= salesLatestStartHour {
		return nil
	}
	date := storage.SalesDate(now)

	salesUsers, err := t.salesUsers(ctx)
	if err != nil {
		return err
	}
	ids := make([]int64, len(salesUsers))
	for i, u := range salesUsers {
		ids[i] = u.TgID
	}
	users, err := t.store.StartSalesDay(ctx, date, ids)
	if err != nil {
		return err
	}
	out, err := t.salesOutput(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		out.send(ctx, u, "Пора отправить отчёт за сегодня — пришли его "+out.sendTo+".", nil)
	}
	t.log.Info("sales day started", "date", date, "asked", len(users), "in_chat", len(out.chats) > 0)

	// Scheduled even when nothing was created now: a retried job must still
	// schedule the next step. Unique args prevent duplicates.
	return t.scheduleSalesStep(ctx, salesStepArgs{Date: date, Step: 1}, salesStep)
}

func (t *Bot) workSalesStep(ctx context.Context, job *river.Job[salesStepArgs]) error {
	args := job.Args
	if args.Step > salesAttempts {
		return t.closeSalesDay(ctx, args.Date)
	}

	reports, err := t.store.AdvanceSalesAttempt(ctx, args.Date, args.Step)
	if err != nil {
		return err
	}
	out, err := t.salesOutput(ctx)
	if err != nil {
		return err
	}
	delay := salesStep
	question := fmt.Sprintf("отчёт за сегодня отправил %s?\n<i>Попытка %d из %d</i>",
		out.answerTo, args.Step, salesAttempts)
	if args.Step == salesAttempts {
		delay = salesGrace
		question += "\n\nЕсли ещё не отправил — время есть до " +
			t.salesStepTime(delay).Format("15:04") + ", потом нажми «Да»."
	}
	for _, r := range reports {
		out.send(ctx, r.User, question, salesButtons(args.Date, r.User.TgID))
	}
	t.log.Info("sales question", "date", args.Date, "attempt", args.Step, "asked", len(reports),
		"next_step_at", t.salesStepTime(delay).Format(time.DateTime))
	return t.scheduleSalesStep(ctx, salesStepArgs{Date: args.Date, Step: args.Step + 1}, delay)
}

func (t *Bot) scheduleSalesStep(ctx context.Context, args salesStepArgs, delay time.Duration) error {
	_, err := river.ClientFromContext[pgx.Tx](ctx).Insert(ctx, args, &river.InsertOpts{
		ScheduledAt: t.salesStepTime(delay),
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	})
	return err
}

// salesStepTime is when a step due after delay runs, moved out of quiet hours.
func (t *Bot) salesStepTime(delay time.Duration) time.Time {
	return outsideQuietHours(time.Now().Add(delay).In(t.tz))
}

func (t *Bot) closeSalesDay(ctx context.Context, date string) error {
	reports, closedNow, err := t.store.CloseSalesDay(ctx, date)
	if err != nil || !closedNow || len(reports) == 0 {
		return err
	}
	day, err := time.ParseInLocation(time.DateOnly, date, t.tz)
	if err != nil {
		return err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "💼 <b>Отчёты продажников за %s</b>\n\n", formatDay(day))
	for _, r := range reports {
		sb.WriteString(displayName(r.User) + " — " + salesStatusText(r) + "\n")
	}

	admins, err := t.adminIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range admins {
		t.notify(ctx, id, sb.String(), nil)
	}
	t.log.Info("sales day closed", "date", date, "reports", len(reports), "admins_notified", len(admins))
	return nil
}

func (t *Bot) onSalesAnswer(ctx context.Context, b *bot.Bot, update *models.Update) {
	cq := update.CallbackQuery
	answer := func(text string) {
		if _, err := b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID, Text: text}); err != nil {
			t.log.Error("answer callback", "err", err)
		}
	}

	// "<date>:<owner>:<y|n>"; buttons sent before chats had no owner: "<date>:<y|n>".
	parts := strings.Split(strings.TrimPrefix(cq.Data, cbSalesPrefix), ":")
	date, choice := parts[0], parts[len(parts)-1]
	if len(parts) == 3 && parts[1] != strconv.FormatInt(cq.From.ID, 10) {
		answer("Это вопрос не тебе 🙂")
		return
	}
	if (len(parts) != 2 && len(parts) != 3) || (choice != "y" && choice != "n") {
		answer("Некорректная кнопка")
		return
	}
	sent := choice == "y"

	r, err := t.store.AnswerSales(ctx, date, cq.From.ID, sent)
	switch {
	case errors.Is(err, storage.ErrClosed):
		answer("Время для ответа вышло — напиши руководителю")
		return
	case errors.Is(err, storage.ErrNotFound):
		answer("Этот отчёт не найден")
		return
	case err != nil:
		t.log.Error("save sales answer", "tg_id", cq.From.ID, "err", err)
		answer("Ошибка, попробуй ещё раз")
		return
	}
	t.log.Info("sales answer", "tg_id", cq.From.ID, "date", date, "sent", sent, "status", r.Status, "attempt", r.AnsweredAttempt)
	if !sent && r.Status == storage.SalesSent {
		// "No" under an earlier question after "yes": the message is redrawn
		// as sent below, which also drops its stale buttons.
		answer("Ты уже ответил «Да» — отчёт засчитан")
	} else {
		answer("Записал")
	}

	m := cq.Message.Message
	if m == nil {
		return
	}
	// In a group everyone sees the message, so it names the person.
	who := ""
	if m.Chat.Type != models.ChatTypePrivate {
		who = displayName(r.User) + ": "
	}
	text := "✅ " + who + "отчёт отправлен. Спасибо!"
	var markup models.ReplyMarkup // nil drops the buttons
	if r.Status != storage.SalesSent {
		text = "❌ " + who + "пока не отправил."
		if r.Attempts < salesAttempts {
			text += " Спрошу ещё раз через 20 минут."
		}
		text += "\nКогда отправишь — нажми кнопку ниже."
		// Keep a way to confirm later, until the day is closed.
		markup = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: "✅ Отправил", CallbackData: fmt.Sprintf("%s%s:%d:y", cbSalesPrefix, date, cq.From.ID)},
		}}}
	}
	_, err = t.api.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID: m.Chat.ID, MessageID: m.ID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: markup,
	})
	if err != nil {
		t.log.Error("edit sales question", "err", err)
	}
}

// salesOut sends sales messages: to the sales chats with a tag, or to DMs.
type salesOut struct {
	t     *Bot
	chats []int64
	// Whom to send the report to, and how to name them in "sent it to …?".
	// In a chat the admin is named without a tag, so they are not pinged
	// on every question.
	sendTo, answerTo string
}

func (t *Bot) salesOutput(ctx context.Context) (salesOut, error) {
	out := salesOut{t: t, sendTo: "руководителю в личку", answerTo: "руководителю"}
	// In test mode the sales chats are skipped: the questions go to DMs.
	if !t.testMode() {
		chats, err := t.store.ChatsFor(ctx, storage.PurposeSales)
		if err != nil {
			return salesOut{}, err
		}
		if len(chats) > 0 {
			out.chats = chats
			return out, nil
		}
	}
	admin, err := t.adminToTag(ctx)
	if err != nil {
		return salesOut{}, err
	}
	if admin != "" {
		out.sendTo, out.answerTo = "в личку "+admin, admin
	}
	return out, nil
}

// send delivers "💼 <text>" to u: in the sales chats as "💼 @u, <text>".
func (o salesOut) send(ctx context.Context, u storage.User, text string, markup models.ReplyMarkup) {
	if len(o.chats) == 0 {
		o.t.notify(ctx, u.TgID, "💼 "+capitalize(text), markup)
		return
	}
	for _, chatID := range o.chats {
		o.t.notify(ctx, chatID, "💼 "+mention(u)+", "+text, markup)
	}
}

func capitalize(s string) string {
	r := []rune(s)
	if len(r) > 0 {
		r[0] = unicode.ToUpper(r[0])
	}
	return string(r)
}

// salesTodayText is the sales part of /today.
func (t *Bot) salesTodayText(ctx context.Context) (string, error) {
	now := time.Now().In(t.tz)
	salesUsers, err := t.salesUsers(ctx)
	if err != nil || len(salesUsers) == 0 {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("💼 <b>Отчёты продажников</b>\n")
	if !salesWorkday(now) {
		sb.WriteString("Сегодня выходной — отчёт не нужен.\n")
		return sb.String(), nil
	}

	reports, err := t.store.SalesDay(ctx, storage.SalesDate(now))
	if err != nil {
		return "", err
	}
	if len(reports) == 0 {
		for _, u := range salesUsers {
			sb.WriteString(displayName(u) + " — 🕓 попрошу отчёт в 18:00\n")
		}
		return sb.String(), nil
	}
	for _, r := range reports {
		sb.WriteString(displayName(r.User) + " — " + salesStatusText(r) + "\n")
	}
	return sb.String(), nil
}

func salesStatusText(r storage.SalesReport) string {
	switch r.Status {
	case storage.SalesSent:
		return fmt.Sprintf("✅ отправил · с %d-й попытки", r.AnsweredAttempt)
	case storage.SalesNotSent:
		if r.Closed {
			return fmt.Sprintf("❌ не отправил · ответ с %d-й попытки", r.AnsweredAttempt)
		}
		return "❌ пока не отправил · ещё может нажать «Да»"
	case storage.SalesNoAnswer:
		return fmt.Sprintf("⚠️ нет ответа (%d попытки)", r.Attempts)
	}
	if r.Attempts == 0 {
		return "📨 попросил отправить отчёт"
	}
	if r.Attempts >= salesAttempts {
		return "⏳ ждём ответа · напоминания закончились, ещё может нажать «Да»"
	}
	return fmt.Sprintf("⏳ ждём ответа · попытка %d из %d", r.Attempts, salesAttempts)
}

// salesButtons are [Yes] [No] that only owner may press.
func salesButtons(date string, owner int64) models.ReplyMarkup {
	data := func(choice string) string { return fmt.Sprintf("%s%s:%d:%s", cbSalesPrefix, date, owner, choice) }
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: "✅ Да", CallbackData: data("y")},
		{Text: "❌ Нет", CallbackData: data("n")},
	}}}
}

func salesWorkday(t time.Time) bool {
	return t.Weekday() != time.Saturday && t.Weekday() != time.Sunday
}

var weekdays = [...]string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}

// formatDay renders a date as "пт 17.10".
func formatDay(t time.Time) string {
	return weekdays[t.Weekday()] + " " + t.Format("02.01")
}
