package telegram

import (
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
		{at(26, 8, 0), at(23, 9, 0)},   // Mon 08:00 → Fri 08:00 → quiet hours → 09:00
		{at(20, 8, 0), at(19, 9, 0)},   // Tue 08:00 → Mon 08:00 → 09:00
		{at(26, 22, 30), at(23, 9, 0)}, // Mon 22:30 → Fri 22:30 → quiet hours → Sat → Fri 09:00
		{at(24, 22, 0), at(23, 9, 0)},  // Sat 22:00 → Fri 22:00 → quiet hours → Sat → Fri 09:00
		{at(21, 23, 0), at(21, 9, 0)},  // Wed 23:00 → Tue 23:00 → quiet hours → Wed 09:00 (a weekday stays)
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

	asked := post
	asked.MaterialStep, asked.MaterialStepAt = 1, at(19, 15, 0)
	check("reminder not yet", asked, at(19, 15, 29), 0, false)
	check("reminder 1", asked, at(19, 15, 30), 2, true)
	asked.MaterialStepAt = at(19, 15, 0).Add(43 * time.Second)
	check("reminder 1 on the minute", asked, at(19, 15, 30), 2, true)

	reminded := post
	reminded.MaterialStep, reminded.MaterialStepAt = 4, at(19, 16, 30)
	check("admin", reminded, at(19, 17, 0), materialAdminStep, true)

	done := post
	done.MaterialStep = materialAdminStep
	check("after the admin", done, at(19, 23, 0), 0, false)

	got := asked
	got.MaterialReceivedAt = at(19, 15, 10)
	check("material came", got, at(19, 16, 0), 0, false)

	check("from the publication on", post, at(20, 15, 0), 0, false)
	check("stories need no material", storage.Task{Type: storage.TaskStories, PublishAt: at(20, 15, 0)}, at(19, 15, 0), 0, false)

	late := post
	late.MaterialStep, late.MaterialStepAt = 2, at(19, 21, 45)
	check("reminder in quiet hours waits", late, at(19, 22, 15), 0, false)
	check("reminder after quiet hours", late, at(20, 9, 0), 3, true)
}

func TestPublishDue(t *testing.T) {
	story := storage.Task{Type: storage.TaskStories, PublishAt: at(22, 11, 0), Status: storage.PublishPending}
	check := func(name string, task storage.Task, now time.Time, wantAct publishAction, wantN int) {
		t.Helper()
		if act, n := publishDue(task, now); act != wantAct || n != wantN {
			t.Errorf("%s: %d %d, want %d %d", name, act, n, wantAct, wantN)
		}
	}

	check("early", story, at(22, 9, 59), publishNone, 0)
	check("an hour before", story, at(22, 10, 0), publishRemind, 1)
	check("missed the first, before the second", story, at(22, 10, 20), publishRemind, 1)
	check("missed both: only the second", story, at(22, 10, 40), publishRemind, 2)
	sent := story
	sent.RemindersSent = 2
	check("both sent", sent, at(22, 10, 50), publishNone, 0)
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

	early := storage.Task{Type: storage.TaskStories, PublishAt: at(23, 6, 0), Status: storage.PublishPending}
	check("reminders in quiet hours are dropped", early, at(23, 5, 30), publishNone, 0)
	check("question waits for the morning", early, at(23, 8, 0), publishNone, 0)
	check("question in the morning", early, at(23, 9, 0), publishAsk, 1)

	quarter := storage.Task{Type: storage.TaskPost, PublishAt: at(23, 9, 15), Status: storage.PublishPending}
	check("both reminders moved to 09:00: the second", quarter, at(23, 9, 0), publishRemind, 2)
}

func TestMinutesText(t *testing.T) {
	for n, want := range map[int]string{1: "1 минуту", 2: "2 минуты", 5: "5 минут", 11: "11 минут", 12: "12 минут", 21: "21 минуту", 22: "22 минуты", 30: "30 минут"} {
		if got := minutesText(n); got != want {
			t.Errorf("minutesText(%d) = %q, want %q", n, got, want)
		}
	}
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
