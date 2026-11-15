package telegram

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"remindbot/internal/storage"
)

var moscow = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		panic(err)
	}
	return loc
}()

// at is a time in October 2026 in Moscow: at(20, 15, 0) is Tue 20.10 15:00.
func at(day, hour, min int) time.Time {
	return time.Date(2026, 10, day, hour, min, 0, 0, moscow)
}

func TestMaterialRequestTime(t *testing.T) {
	for _, tc := range []struct{ publish, want time.Time }{
		{at(20, 15, 0), at(19, 15, 0)}, // Tue → Mon
		{at(26, 15, 0), at(23, 15, 0)}, // Mon → Sun → Fri
		{at(25, 12, 0), at(23, 12, 0)}, // Sun → Sat → Fri
		{at(24, 12, 0), at(23, 12, 0)}, // Sat → Fri anyway
		{at(26, 8, 0), at(23, 8, 0)},   // Mon 08:00 → Fri 08:00, after quiet hours
		{at(20, 5, 30), at(19, 6, 0)},  // Tue 05:30 → Mon 05:30 → 06:00
		{at(26, 22, 30), at(23, 6, 0)}, // Mon 22:30 → Fri 22:30 → quiet hours → Sat → Fri 06:00
		{at(24, 22, 0), at(23, 6, 0)},  // Sat 22:00 → Fri 22:00 → quiet hours → Sat → Fri 06:00
		{at(21, 23, 0), at(21, 6, 0)},  // Wed 23:00 → Tue 23:00 → quiet hours → Wed 06:00 (a weekday stays)
	} {
		if got := materialRequestTime(tc.publish); !got.Equal(tc.want) {
			t.Errorf("publish %s: request %s, want %s", tc.publish.Format(time.DateTime), got.Format(time.DateTime), tc.want.Format(time.DateTime))
		}
	}
}

func TestMaterialDue(t *testing.T) {
	post := storage.Task{Type: storage.TaskPost, PublishAt: at(20, 15, 0)}
	check := func(name string, task storage.Task, now time.Time, wantStep int, wantDue bool) {
		t.Helper()
		step, due := materialDue(task, now)
		if due != wantDue || (due && step != wantStep) {
			t.Errorf("%s: step %d due %v, want %d %v", name, step, due, wantStep, wantDue)
		}
	}

	check("before the request", post, at(19, 14, 59), 0, false)
	check("request", post, at(19, 15, 0), 1, true)
	check("request a little late", post, at(19, 15, 30), 1, true)
	check("request too late: none", post, at(19, 15, 31), 0, false)
	check("request day missed: none", post, at(20, 10, 0), 0, false)
	if !materialRequestMissed(post, at(19, 15, 31)) || materialRequestMissed(post, at(19, 15, 30)) {
		t.Error("materialRequestMissed disagrees with materialDue")
	}

	// Since 16.11.2026 the material is asked for once: no reminder.
	asked := post
	asked.MaterialStep, asked.MaterialStepAt = 1, at(19, 15, 0)
	check("no reminder an hour later", asked, at(19, 16, 0), 0, false)
	check("no reminder later", asked, at(19, 23, 0), 0, false)

	done := post
	done.MaterialStep, done.MaterialStepAt = materialLastStep, at(19, 16, 0)
	check("nothing after the reminder, no admin", done, at(19, 23, 0), 0, false)
	old := post
	old.MaterialStep, old.MaterialStepAt = 4, at(19, 16, 30)
	check("old rows with more steps stop", old, at(19, 17, 30), 0, false)

	got := asked
	got.MaterialReceivedAt = at(19, 15, 10)
	check("material came", got, at(19, 16, 0), 0, false)

	check("from the publication on", post, at(20, 15, 0), 0, false)
	check("stories need no material", storage.Task{Type: storage.TaskStories, PublishAt: at(20, 15, 0)}, at(19, 15, 0), 0, false)

}

func TestPublishDue(t *testing.T) {
	story := storage.Task{Type: storage.TaskStories, PublishAt: at(22, 11, 0), Status: storage.PublishPending}
	check := func(name string, task storage.Task, now time.Time, wantAct publishAction, wantN int) {
		t.Helper()
		if act, n := publishDue(task, now); act != wantAct || n != wantN {
			t.Errorf("%s: %d %d, want %d %d", name, act, n, wantAct, wantN)
		}
	}

	check("an hour before: nothing", story, at(22, 10, 0), publishNone, 0)
	check("early", story, at(22, 10, 29), publishNone, 0)
	check("half an hour before", story, at(22, 10, 30), publishRemind, 1)
	check("late but before P", story, at(22, 10, 50), publishRemind, 1)
	sent := story
	sent.RemindersSent = 1
	check("sent", sent, at(22, 10, 50), publishNone, 0)
	old := story
	old.RemindersSent = 2 // from when there were two reminders
	check("old rows with two sent", old, at(22, 10, 50), publishNone, 0)
	check("at P", story, at(22, 11, 0), publishNone, 0)

	check("question 1", story, at(22, 11, 30), publishAsk, 1)
	check("question 1 late but fresh", story, at(22, 15, 30), publishAsk, 1)
	check("question 1 too late", story, at(22, 15, 31), publishNone, 0)

	asked := story
	asked.Attempts, asked.AskedAt = 1, at(22, 11, 30)
	check("question 2 not yet", asked, at(22, 11, 59), publishNone, 0)
	check("question 2", asked, at(22, 12, 0), publishAsk, 2)
	asked.AskedAt = at(22, 11, 30).Add(43 * time.Second) // the tick runs at :43
	check("question 2 on the minute, not a minute late", asked, at(22, 12, 0), publishAsk, 2)
	asked.Attempts, asked.AskedAt = 4, at(22, 13, 0)
	check("no answer", asked, at(22, 13, 30), publishNoAnswer, 0)

	answered := asked
	answered.Status = storage.PublishDone
	check("answered", answered, at(22, 13, 30), publishNone, 0)

	early := storage.Task{Type: storage.TaskStories, PublishAt: at(23, 5, 0), Status: storage.PublishPending}
	check("reminders in quiet hours are dropped", early, at(23, 4, 30), publishNone, 0)
	check("question waits for the morning", early, at(23, 5, 59), publishNone, 0)
	check("question in the morning", early, at(23, 6, 0), publishAsk, 1)

	quarter := storage.Task{Type: storage.TaskPost, PublishAt: at(23, 6, 15), Status: storage.PublishPending}
	check("reminder moved to 06:00, still before P", quarter, at(23, 6, 0), publishRemind, 1)

	// Stories at 06:00: the reminder at 05:30 is in quiet hours and would
	// move past P, so there is none.
	six := storage.Task{Type: storage.TaskStories, PublishAt: at(23, 6, 0), Status: storage.PublishPending}
	check("06:00 stories: no reminder", six, at(23, 6, 0), publishNone, 0)
	seven := storage.Task{Type: storage.TaskStories, PublishAt: at(23, 7, 0), Status: storage.PublishPending}
	check("07:00 stories get the 06:30 reminder", seven, at(23, 6, 30), publishRemind, 1)
}

func TestWhenText(t *testing.T) {
	now := at(22, 9, 0) // Thu
	for p, want := range map[time.Time]string{
		at(22, 11, 0): "сегодня в 11:00",
		at(23, 11, 0): "завтра в 11:00",
		at(26, 15, 0): "в пн 26.10 в 15:00",
	} {
		if got := whenText(p, now); got != want {
			t.Errorf("whenText(%s) = %q, want %q", p.Format(time.DateTime), got, want)
		}
	}
}

func TestHasMaterial(t *testing.T) {
	link := []models.MessageEntity{{Type: models.MessageEntityTypeURL}}
	for name, tc := range map[string]struct {
		m    models.Message
		want bool
	}{
		"plain text":      {models.Message{Text: "скинула в личку"}, false},
		"photo":           {models.Message{Photo: []models.PhotoSize{{FileID: "x"}}}, true},
		"document":        {models.Message{Document: &models.Document{FileID: "x"}}, true},
		"link in text":    {models.Message{Text: "https://disk.yandex.ru/x", Entities: link}, true},
		"link in caption": {models.Message{Caption: "вот", CaptionEntities: link}, true},
	} {
		if got := hasMaterial(&tc.m); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

func TestJudgeReply(t *testing.T) {
	now := at(22, 10, 0)
	post := storage.Task{Type: storage.TaskPost, PublishAt: at(22, 11, 0)}
	got := post
	got.MaterialReceivedAt = at(22, 9, 0)
	published := post
	published.PublishAt = at(22, 9, 0)
	file := &models.Message{Document: &models.Document{FileID: "x"}}
	text := &models.Message{Text: "ок, спасибо"}
	for name, tc := range map[string]struct {
		task storage.Task
		kind string
		msg  *models.Message
		want replyVerdict
	}{
		"file to the material request":    {post, storage.TaskMsgMaterial, file, replyMaterial},
		"text to the material request":    {post, storage.TaskMsgMaterial, text, replyNeedsFile},
		"file to a reminder before P":     {post, storage.TaskMsgPublish, file, replyMaterial},
		"text to a reminder":              {post, storage.TaskMsgPublish, text, replyIgnore},
		"file to «published?» after P":    {published, storage.TaskMsgPublish, file, replyIgnore},
		"«ok, thanks» after the material": {got, storage.TaskMsgMaterial, text, replyAlreadyReceived},
		"another file after the material": {got, storage.TaskMsgMaterial, file, replyAlreadyReceived},
		"stories take no material":        {storage.Task{Type: storage.TaskStories, PublishAt: at(22, 11, 0)}, storage.TaskMsgPublish, file, replyIgnore},
	} {
		if v := judgeReply(tc.task, tc.kind, tc.msg, now); v != tc.want {
			t.Errorf("%s: %d, want %d", name, v, tc.want)
		}
	}
}

func TestTelegramDayDue(t *testing.T) {
	tg := func(id string, p time.Time) storage.Task {
		return storage.Task{ID: id, Type: storage.TaskTelegram, Title: id, PublishAt: p, Status: storage.PublishPending}
	}
	ids := func(ts []storage.Task) string {
		var out []string
		for _, x := range ts {
			out = append(out, x.ID)
		}
		return strings.Join(out, ",")
	}
	day := []storage.Task{
		tg("t18", at(22, 18, 0)), tg("t16", at(22, 16, 0)), tg("t17", at(22, 17, 0)),
		tg("tomorrow", at(23, 16, 0)),
		{ID: "story", Type: storage.TaskStories, PublishAt: at(22, 16, 0), Status: storage.PublishPending},
	}
	check := func(name string, tasks []storage.Task, now time.Time, want string) {
		t.Helper()
		if got := ids(telegramDayDue(tasks, now)); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	check("before the hour", day, at(22, 14, 59), "")
	check("an hour before the first: the day's posts in order", day, at(22, 15, 0), "t16,t17,t18")
	check("late, before the first", day, at(22, 15, 40), "t16,t17,t18")

	sent := slices.Clone(day)
	for i := range sent {
		if sent[i].Type == storage.TaskTelegram && sameDay(sent[i].PublishAt, at(22, 0, 0)) {
			sent[i].RemindersSent = 1
		}
	}
	check("already announced", sent, at(22, 15, 30), "")
	// A post added later in the day gets a message of its own an hour before it.
	added := append(slices.Clone(sent), tg("t20", at(22, 20, 0)))
	check("added: not yet", added, at(22, 18, 59), "")
	check("added: its hour", added, at(22, 19, 0), "t20")

	// The bot was down until 16:30: the posts still ahead are announced.
	check("after the first post", day, at(22, 16, 30), "t17,t18")
	// Quiet hours: a 06:30 post is announced at 06:00, not 05:30.
	early := []storage.Task{tg("t0630", at(22, 6, 30))}
	check("quiet hours: before 06:00", early, at(22, 5, 59), "")
	check("quiet hours: at 06:00", early, at(22, 6, 0), "t0630")

	// No 30-minute reminder for Telegram: the day's message instead.
	if act, _ := publishDue(tg("x", at(22, 16, 0)), at(22, 15, 30)); act != publishNone {
		t.Errorf("Telegram post got the 30-minute reminder: %d", act)
	}
}

func TestTelegramDayText(t *testing.T) {
	post := func(title string, p time.Time) storage.Task {
		return storage.Task{Type: storage.TaskTelegram, Title: title, PublishAt: p}
	}
	got := telegramDayText("@tg", []storage.Task{post("скидки", at(22, 16, 0)), post("кофе & вкус", at(22, 17, 0))}, moscow)
	want := "🔔 @tg, посты в телеграме сегодня:\n• <b>16:00</b> «скидки»\n• <b>17:00</b> «кофе &amp; вкус»"
	if got != want {
		t.Errorf("two posts:\n%s\nwant\n%s", got, want)
	}
	got = telegramDayText("@tg", []storage.Task{post("афиша", at(22, 16, 0))}, moscow)
	if want := "🔔 @tg, в 16:00 выложить пост в телеграме «афиша»"; got != want {
		t.Errorf("one post: %q, want %q", got, want)
	}
}
