package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/riverqueue/river"

	"remindbot/internal/storage"
)

// Reminders about publications from YouGile: what is due when is decided in
// tasks_schedule.go, the copy of the tasks is kept by yougile_sync.go.
//
// Assignees from YouGile are not linked to Telegram yet, so messages name
// them without a tag; a task without assignees is for the primary admin.

// Callback data: "pub:<task id>:<publish time, Unix>:y|n". The time binds the
// answer to the publication asked about: the task may be moved since.
// Buttons sent before had no time: "pub:<task id>:y|n".
const cbPublishPrefix = "pub:"

// The tick looks at tasks published within this window around now: the
// material for a Monday post is asked for on Friday, and quiet hours can move
// the last question to the next morning.
const (
	tickPast  = 24 * time.Hour
	tickAhead = 4 * 24 * time.Hour
)

type tasksTickArgs struct{}

func (tasksTickArgs) Kind() string { return "tasks_tick" }

// taskJobs adds the YouGile jobs; there are none without an API key.
func (t *Bot) taskJobs(workers *river.Workers) ([]*river.PeriodicJob, error) {
	if t.yg == nil {
		return nil, nil
	}
	sync, err := t.yougileSyncJob(workers)
	if err != nil {
		return nil, err
	}
	river.AddWorker(workers, river.WorkFunc(t.workTasksTick))
	tick := river.NewPeriodicJob(river.PeriodicInterval(time.Minute),
		func() (river.JobArgs, *river.InsertOpts) { return tasksTickArgs{}, nil },
		&river.PeriodicJobOpts{ID: "tasks_tick"})
	return []*river.PeriodicJob{sync, tick}, nil
}

func (t *Bot) workTasksTick(ctx context.Context, _ *river.Job[tasksTickArgs]) error {
	now := time.Now().In(t.tz)
	t.checkSyncStale(ctx, now)
	// Overdue steps wait for the morning too.
	if inQuietHours(now) {
		return nil
	}
	tasks, err := t.store.ActiveTasks(ctx, now.Add(-tickPast), now.Add(tickAhead))
	if err != nil {
		return err
	}
	t.log.Debug("tasks tick", "tasks", len(tasks))
	t.forgetOldSkips(now)
	// A failed step is logged and skipped: the next tick tries again unless
	// the step was claimed.
	for _, task := range tasks {
		t.logSkipped(task, now)
		if step, due := materialDue(task, now); due {
			if err := t.sendMaterialStep(ctx, task, step); err != nil {
				t.log.Error("material step", "task", task.ID, "step", step, "err", err)
			}
		}
		act, n := publishDue(task, now)
		if !t.features.PublishQuestions && (act == publishAsk || act == publishNoAnswer) {
			act = publishNone
		}
		if act != publishNone {
			if err := t.sendPublishStep(ctx, task, act, n, now); err != nil {
				t.log.Error("publish step", "task", task.ID, "act", act, "err", err)
			}
		}
	}
	return nil
}

// The step senders read everything they need — the text and the chats —
// before claiming the step: once it is claimed, a failed query would lose the
// message for good.

func (t *Bot) sendMaterialStep(ctx context.Context, task storage.Task, step int) error {
	text, err := t.materialText(ctx, task, step)
	if err != nil {
		return err
	}
	chats, err := t.contentChats(ctx)
	if err != nil {
		return err
	}
	ok, err := t.store.AdvanceMaterial(ctx, task.ID, step-1)
	if err != nil || !ok {
		if err == nil {
			t.log.Debug("material step already taken", "task", task.ID, "step", step)
		}
		return err
	}
	t.log.Info("material step", "task", task.ID, "title", task.Title, "type", task.Type,
		"publish_at", task.PublishAt.In(t.tz).Format(time.DateTime), "step", step, "of", materialLastStep)
	if !t.sendAboutTask(ctx, chats, task, storage.TaskMsgMaterial, text, nil) {
		// Nothing went: give the step back, the next tick tries again.
		if err := t.store.ReleaseMaterial(ctx, task.ID, step-1, task.MaterialStepAt); err != nil {
			return err
		}
		t.log.Warn("material step not delivered, will retry", "task", task.ID, "step", step)
	}
	return nil
}

func (t *Bot) materialText(ctx context.Context, task storage.Task, step int) (string, error) {
	p := task.PublishAt.In(t.tz)
	title := html.EscapeString(task.Title)
	switch {
	case step == 1:
		who, err := t.taskWho(ctx, task)
		if err != nil {
			return "", err
		}
		return "📎 " + addressed(who, fmt.Sprintf(
			"%s %s «%s». Пришли готовый согласованный материал — файл или ссылку — <b>ответом на это сообщение</b>.",
			whenText(p, time.Now().In(t.tz)), task.Type.Noun(), title)) +
			"\n💡 Не забудь про сторис и обложку.", nil
	}
	who, err := t.taskWho(ctx, task)
	if err != nil {
		return "", err
	}
	return "📎 " + addressed(who, fmt.Sprintf(
		"напоминание: жду материал для %s «%s» (%s) — файл или ссылку ответом на это сообщение.",
		task.Type.NounOf(), title, dayTime(p))), nil
}

func (t *Bot) sendPublishStep(ctx context.Context, task storage.Task, act publishAction, n int, now time.Time) error {
	if act == publishNoAnswer {
		closed, err := t.store.CloseUnanswered(ctx, task.ID, task.Attempts)
		if closed {
			t.log.Info("publish: no answer", "task", task.ID, "title", task.Title, "attempts", task.Attempts)
		}
		return err
	}

	p := task.PublishAt.In(t.tz)
	what := task.Type.Noun() + " «" + html.EscapeString(task.Title) + "»"
	who, err := t.taskWho(ctx, task)
	if err != nil {
		return err
	}
	chats, err := t.contentChats(ctx)
	if err != nil {
		return err
	}
	step, text, markup := "reminder", "🔔 "+addressed(who, "в "+clock(p)+" выложить "+what), models.ReplyMarkup(nil)
	switch {
	case act == publishAsk:
		step, markup = "question", publishButtons(task)
		text = fmt.Sprintf("❓ %s\n<i>Попытка %d из %d</i>",
			addressed(who, what+" ("+clock(p)+") "+task.Type.Published()+"?"), n, publishAttempts)
	case n > 1:
		mins := max(1, int(p.Sub(now.Truncate(time.Minute)).Minutes()))
		text = "🔔 " + addressed(who, "через "+minutesText(mins)+" "+what)
	}

	var claimed bool
	if act == publishAsk {
		claimed, err = t.store.AdvancePublishAttempt(ctx, task.ID, task.Attempts)
	} else {
		claimed, err = t.store.AdvanceReminders(ctx, task.ID, task.RemindersSent, n)
	}
	if err != nil || !claimed {
		if err == nil {
			t.log.Debug("publish step already taken", "task", task.ID, "step", step, "n", n)
		}
		return err
	}
	t.log.Info("publish "+step, "task", task.ID, "title", task.Title, "type", task.Type,
		"publish_at", p.Format(time.DateTime), "n", n)
	if !t.sendAboutTask(ctx, chats, task, storage.TaskMsgPublish, text, markup) {
		// Nothing went: give the step back, the next tick tries again.
		if act == publishAsk {
			err = t.store.ReleasePublishAttempt(ctx, task.ID, task.Attempts, task.AskedAt)
		} else {
			err = t.store.ReleaseReminders(ctx, task.ID, task.RemindersSent, n)
		}
		if err != nil {
			return err
		}
		t.log.Warn("publish step not delivered, will retry", "task", task.ID, "step", step, "n", n)
	}
	return nil
}

// logSkipped logs, once per task and publish time, a step the bot will not
// take because its time has passed: so the log tells why nothing was sent.
func (t *Bot) logSkipped(task storage.Task, now time.Time) {
	once := func(kind, msg string, attrs ...any) {
		key := kind + ":" + task.ID + ":" + task.PublishAt.UTC().Format(time.RFC3339)
		if _, seen := t.skipsLogged.LoadOrStore(key, task.PublishAt); !seen {
			t.log.Info(msg, append([]any{"task", task.ID, "title", task.Title,
				"publish_at", task.PublishAt.In(t.tz).Format(time.DateTime)}, attrs...)...)
		}
	}
	p := task.PublishAt.In(t.tz)
	if materialRequestMissed(task, now) {
		once("material", "material request skipped: its time has passed",
			"request_at", materialRequestTime(p).Format(time.DateTime))
	}
	if ask := outsideQuietHours(p.Add(taskStep)); t.features.PublishQuestions && task.Status == storage.PublishPending &&
		task.Attempts == 0 && now.Sub(ask) > askCatchUp {
		once("ask", "publish question skipped: too late to ask", "ask_at", ask.Format(time.DateTime))
	}
}

// forgetOldSkips drops the skip marks of tasks the tick no longer looks at,
// so they do not pile up.
func (t *Bot) forgetOldSkips(now time.Time) {
	t.skipsLogged.Range(func(key, publishAt any) bool {
		if publishAt.(time.Time).Before(now.Add(-tickPast)) {
			t.skipsLogged.Delete(key)
		}
		return true
	})
}

// sendAboutTask posts a message about the task to the chats and remembers it,
// so that a reply can be matched to the task. It reports whether anything was
// delivered; with no chats at all there is nothing to retry, so that counts.
func (t *Bot) sendAboutTask(ctx context.Context, chats []int64, task storage.Task, kind, text string, markup models.ReplyMarkup) (delivered bool) {
	if len(chats) == 0 {
		t.log.Warn("no content chats to send to; assign one in /chats", "task", task.ID, "text", preview(text))
		return true
	}
	for _, chatID := range chats {
		for _, m := range t.notify(ctx, chatID, text, markup) {
			delivered = true
			if err := t.store.AddTaskMessage(ctx, m.Chat.ID, m.ID, task.ID, kind); err != nil {
				t.log.Error("save task message", "task", task.ID, "err", err)
			}
		}
	}
	return delivered
}

// taskWho names whom a message about the task is for: its assignees by name,
// or the primary admin with a tag when there are none.
func (t *Bot) taskWho(ctx context.Context, task storage.Task) (string, error) {
	if len(task.Assignees) == 0 {
		return t.adminToTag(ctx)
	}
	return t.assigneeNames(ctx, task)
}

// assigneeNames lists the task's assignees by their YouGile names, HTML-escaped.
func (t *Bot) assigneeNames(ctx context.Context, task storage.Task) (string, error) {
	if len(task.Assignees) == 0 {
		return "", nil
	}
	names, err := t.store.YouGileNames(ctx, task.Assignees)
	if err != nil {
		return "", err
	}
	return joinNames(task.Assignees, names), nil
}

// joinNames lists YouGile users by name, HTML-escaped; "" for none.
func joinNames(ids []string, names map[string]string) string {
	var out []string
	for _, id := range ids {
		if name := names[id]; name != "" {
			out = append(out, html.EscapeString(name))
		}
	}
	if len(out) == 0 && len(ids) > 0 {
		return "исполнитель из YouGile"
	}
	return strings.Join(out, ", ")
}

func publishButtons(task storage.Task) models.ReplyMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: "✅ Да", CallbackData: publishData(task, "y")},
		{Text: "❌ Нет", CallbackData: publishData(task, "n")},
	}}}
}

func publishData(task storage.Task, choice string) string {
	return fmt.Sprintf("%s%s:%d:%s", cbPublishPrefix, task.ID, task.PublishAt.Unix(), choice)
}

func (t *Bot) onPublishAnswer(ctx context.Context, b *bot.Bot, update *models.Update) {
	cq := update.CallbackQuery
	answer := func(text string) {
		if _, err := b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID, Text: text}); err != nil {
			t.log.Error("answer callback", "err", err)
		}
	}

	parts := strings.Split(strings.TrimPrefix(cq.Data, cbPublishPrefix), ":")
	taskID, choice := parts[0], parts[len(parts)-1]
	if (len(parts) != 2 && len(parts) != 3) || (choice != "y" && choice != "n") {
		answer("Некорректная кнопка")
		return
	}
	published := choice == "y"

	if len(parts) == 3 {
		task, err := t.store.GetTask(ctx, taskID)
		if err == nil && strconv.FormatInt(task.PublishAt.Unix(), 10) != parts[1] {
			t.onMovedTaskAnswer(ctx, answer, cq, task)
			return
		}
	}
	task, err := t.store.AnswerPublished(ctx, taskID, published)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		answer("Эта задача не найдена")
		return
	case err != nil:
		t.log.Error("save publish answer", "task", taskID, "err", err)
		answer("Ошибка, попробуй ещё раз")
		return
	}
	t.log.Info("publish answer", "task", taskID, "tg_id", cq.From.ID, "published", published, "status", task.Status)
	if !published && task.Status == storage.PublishDone {
		answer("Уже отмечено, что выложено")
	} else {
		answer("Записал")
	}

	m := cq.Message.Message
	if m == nil {
		return
	}
	// In a group everyone sees the message, so it names who answered.
	who := ""
	if m.Chat.Type != models.ChatTypePrivate {
		who = displayName(storage.User{Username: cq.From.Username, FirstName: cq.From.FirstName}) + ": "
	}
	what := task.Type.Noun() + " «" + html.EscapeString(task.Title) + "»"
	text := "✅ " + who + what + " " + task.Type.Published() + ". Спасибо!"
	var markup models.ReplyMarkup // nil drops the buttons
	if task.Status != storage.PublishDone {
		text = "❌ " + who + what + " пока не " + task.Type.Published() + ".\nКогда выложишь — нажми кнопку ниже."
		markup = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: "✅ Выложено", CallbackData: publishData(task, "y")},
		}}}
	}
	_, err = t.api.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID: m.Chat.ID, MessageID: m.ID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: markup,
	})
	if err != nil {
		t.log.Error("edit publish question", "err", err)
	}
}

// onMovedTaskAnswer handles a button under a question about a publication
// that has since moved in YouGile: the answer is not recorded, the bot asks
// again at the new time.
func (t *Bot) onMovedTaskAnswer(ctx context.Context, answer func(string), cq *models.CallbackQuery, task storage.Task) {
	when := dayTime(task.PublishAt.In(t.tz))
	t.log.Info("publish answer to a moved task ignored", "task", task.ID, "title", task.Title,
		"tg_id", cq.From.ID, "publish_at", task.PublishAt.In(t.tz).Format(time.DateTime))
	answer("Публикацию перенесли на " + when + " — спрошу заново")
	m := cq.Message.Message
	if m == nil {
		return
	}
	text := "↪️ " + task.Type.Noun() + " «" + html.EscapeString(task.Title) + "» перенесли на " + when + " — спрошу заново."
	_, err := t.api.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID: m.Chat.ID, MessageID: m.ID, Text: text, ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		t.log.Error("edit publish question", "err", err)
	}
}

// isReplyToBot matches messages written in reply to one of the bot's messages.
func (t *Bot) isReplyToBot(update *models.Update) bool {
	m := update.Message
	return m != nil && m.From != nil && m.ReplyToMessage != nil &&
		m.ReplyToMessage.From != nil && m.ReplyToMessage.From.ID == t.botID
}

// onTaskReply takes the material for a post or reels: a file or a link sent
// in reply to any bot message about the task. In a group the bot sees such
// replies even in privacy mode.
func (t *Bot) onTaskReply(ctx context.Context, _ *bot.Bot, update *models.Update) {
	msg := update.Message
	taskID, kind, err := t.store.TaskForMessage(ctx, msg.Chat.ID, msg.ReplyToMessage.ID)
	if errors.Is(err, storage.ErrNotFound) {
		return
	}
	if err != nil {
		t.log.Error("load task message", "err", err)
		return
	}
	task, err := t.store.GetTask(ctx, taskID)
	if err != nil {
		t.log.Error("load task", "task", taskID, "err", err)
		return
	}

	// An album comes as several messages: only the first one gets an answer.
	inAlbum := msg.MediaGroupID != ""
	received := func() {
		t.log.Info("material already received", "task", taskID, "tg_id", msg.From.ID, "content", contentKinds(msg))
		// "Ok, thanks" after the material needs no answer.
		if hasMaterial(msg) && !inAlbum {
			t.reply(ctx, msg, "✅ Материал уже получен.")
		}
	}
	switch judgeReply(task, kind, msg, time.Now()) {
	case replyIgnore:
		return
	case replyAlreadyReceived:
		received()
		return
	case replyNeedsFile:
		t.log.Info("material reply without a file or link", "task", taskID, "tg_id", msg.From.ID, "content", contentKinds(msg))
		t.reply(ctx, msg, "Нужен файл или ссылка — пришли материал ответом на сообщение бота.")
		return
	}

	task, first, err := t.store.ReceiveMaterial(ctx, taskID, msg.From.ID)
	if err != nil {
		t.log.Error("save material", "task", taskID, "err", err)
		return
	}
	if !first {
		received() // another message got there first
		return
	}
	t.log.Info("material received", "task", taskID, "title", task.Title, "tg_id", msg.From.ID, "content", contentKinds(msg))
	sender := displayName(storage.User{Username: msg.From.Username, FirstName: msg.From.FirstName})
	t.reply(ctx, msg, fmt.Sprintf("✅ Материал для %s «%s» получен от %s.",
		task.Type.NounOf(), html.EscapeString(task.Title), sender))
}

type replyVerdict int

const (
	replyIgnore replyVerdict = iota
	replyAlreadyReceived
	replyNeedsFile
	replyMaterial
)

// judgeReply decides what a reply to a bot message about the task is. Only
// posts and reels take material. A reply to a reminder or a "published?"
// question is about the material only if it carries a file or a link, and
// only before the publication; otherwise it is ignored.
func judgeReply(task storage.Task, kind string, msg *models.Message, now time.Time) replyVerdict {
	switch {
	case !task.Type.NeedsMaterial():
		return replyIgnore
	case kind == storage.TaskMsgPublish && (!hasMaterial(msg) || !now.Before(task.PublishAt)):
		return replyIgnore
	case !task.MaterialReceivedAt.IsZero():
		return replyAlreadyReceived
	case !hasMaterial(msg):
		return replyNeedsFile
	}
	return replyMaterial
}

// hasMaterial reports whether the message carries a file or a link; plain
// text does not count as material.
func hasMaterial(m *models.Message) bool {
	return len(m.Photo) > 0 || m.Video != nil || m.Document != nil || m.Animation != nil ||
		m.VideoNote != nil || m.Audio != nil || hasLink(m)
}

func hasLink(m *models.Message) bool {
	for _, e := range append(m.Entities, m.CaptionEntities...) {
		if e.Type == models.MessageEntityTypeURL || e.Type == models.MessageEntityTypeTextLink {
			return true
		}
	}
	return false
}

// addressed puts who in front of text: "Катя, в 11:00 …"; without anyone
// to address the text is capitalized.
func addressed(who, text string) string {
	if who == "" {
		return capitalize(text)
	}
	return who + ", " + text
}

func clock(t time.Time) string {
	return t.Format("15:04")
}

// dayTime renders "вт 15:00".
func dayTime(t time.Time) string {
	return weekdays[t.Weekday()] + " " + clock(t)
}

// whenText says when p is, from now: "сегодня в 15:00", "завтра в 15:00" or
// "в пн 26.10 в 15:00".
func whenText(p, now time.Time) string {
	switch daysBetween(now, p) {
	case 0:
		return "сегодня в " + clock(p)
	case 1:
		return "завтра в " + clock(p)
	}
	return "в " + formatDay(p) + " в " + clock(p)
}

// minutesText renders "N минут" with the right ending.
func minutesText(n int) string {
	return fmt.Sprintf("%d %s", n, plural(n, "минуту", "минуты", "минут"))
}
