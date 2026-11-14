package telegram

import (
	"strings"
	"testing"
	"time"

	"remindbot/internal/storage"
)

// A week like the first one in production: Saturday 12:30, some answered,
// some moved, some ahead.
func sampleWeek() ([]storage.Task, map[string]string, []storage.SalesReport, time.Time) {
	now := at(24, 12, 30) // Sat
	names := map[string]string{"dj": "Вера", "am": "Лена", "ks": "Катя", "yu": "Таня"}
	task := func(typ storage.TaskType, title string, p time.Time, who ...string) storage.Task {
		return storage.Task{ID: title, Type: typ, Title: title, PublishAt: p, Assignees: who, Status: storage.PublishPending}
	}
	done := func(x storage.Task, attempt int) storage.Task {
		x.Status, x.Attempts, x.AnsweredAttempt = storage.PublishDone, attempt, attempt
		return x
	}
	tasks := []storage.Task{
		task(storage.TaskTelegram, "отель в горах", at(21, 15, 0)),
		done(task(storage.TaskPost, "афиши", at(22, 11, 0), "ks", "yu"), 4),
		done(task(storage.TaskStories, "репост афиш", at(23, 8, 0), "ks"), 3),
		func() storage.Task {
			x := done(task(storage.TaskPost, "осенние напитки", at(23, 11, 0), "dj"), 3)
			x.MaterialStep = materialAdminStep
			return x
		}(),
		func() storage.Task {
			x := task(storage.TaskStories, "сторис партнёра", at(24, 6, 0))
			x.Status, x.Attempts = storage.PublishNoAnswer, 4
			return x
		}(),
		done(task(storage.TaskStories, "подводка к напиткам", at(24, 8, 0), "dj"), 1),
		func() storage.Task {
			x := task(storage.TaskReels, "летние коктейли", at(24, 13, 0), "dj", "am")
			x.RemindersSent, x.MaterialReceivedAt = 1, at(23, 13, 20)
			return x
		}(),
		task(storage.TaskTelegram, "новое меню", at(24, 15, 0)),
		task(storage.TaskReels, "трендовые десерты", at(25, 13, 0), "dj"),
		task(storage.TaskPost, "гид по городу", at(26, 11, 0), "dj", "am"),
	}
	sales := []storage.SalesReport{
		{Date: "2026-10-22", User: storage.User{Username: "smm_lead"}, Status: storage.SalesNoAnswer, Attempts: 2},
		{Date: "2026-10-23", User: storage.User{Username: "smm_lead"}, Status: storage.SalesSent, AnsweredAttempt: 1},
	}
	return tasks, names, sales, now
}

func TestTodayText(t *testing.T) {
	tasks, names, _, now := sampleWeek()
	var today []storage.Task
	for _, x := range tasks {
		if !x.PublishAt.Before(startOfDay(now)) {
			today = append(today, x)
		}
	}
	got := todayText(today, names, now)
	t.Log("\n" + got)
	for _, want := range []string{
		"📌 <b>Публикации · 4</b>",
		"✅ выложено 1 · ⚠️ нет ответа 1 · 🕓 впереди 2",
		"<b>06:00</b> 📱 Сторис «сторис партнёра»\n└ без исполнителя · ⚠️ нет ответа на 4 вопроса",
		"<b>08:00</b> 📱 Сторис «подводка к напиткам»\n└ Вера · ✅ выложено, ответ с 1-й попытки",
		"<b>13:00</b> 🎬 Рилс «летние коктейли»\n└ Вера, Лена · 🔔 напомнил\n└ 📎 материал: ✅ получен",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	// Material for Sunday and Monday is asked for on Friday, not on Saturday.
	if strings.Contains(got, "Материал на согласование") {
		t.Errorf("no material is asked for on Saturday:\n%s", got)
	}

	// On Thursday the material for Friday's post is asked for.
	thu := at(22, 12, 30)
	friday := storage.Task{ID: "f", Type: storage.TaskPost, Title: "осенние напитки", PublishAt: at(23, 11, 0),
		Assignees: []string{"dj"}, Status: storage.PublishPending, MaterialStep: 3, MaterialStepAt: at(22, 12, 0)}
	got = todayText([]storage.Task{friday}, names, thu)
	t.Log("\n" + got)
	want := "📌 Публикаций сегодня нет.\n\n📎 <b>Материал на согласование</b> · запрос сегодня\n\n" +
		"<b>пт 11:00</b> 🖼 Пост «осенние напитки»\n└ Вера · ⏳ ждём · напоминаний 2 из 3"
	if got != want {
		t.Errorf("Thursday:\n%s\nwant\n%s", got, want)
	}
}

func TestWeekText(t *testing.T) {
	tasks, names, sales, now := sampleWeek()
	var week []storage.Task
	for _, x := range tasks {
		if x.PublishAt.Before(startOfWeek(now).AddDate(0, 0, 7)) {
			week = append(week, x)
		}
	}
	got := weekText(week, names, sales, now, true)
	t.Log("\n" + got)
	for _, want := range []string{
		"📊 <b>Неделя пн 19.10 – вс 25.10</b>\n<i>на сб 24.10, 12:30</i>",
		"📌 <b>Публикации · 9</b>",
		"«Да» с попытки: 1-й — 1 · 2-й — 0 · 3-й — 2 · 4-й — 1",
		"пн 19.10 — нет",
		"<b>сб 24.10</b> · 4 — ✅ 1 · ⚠️ 1 · 🕓 2",
		"📱 Сторис · 3 — ✅ 2 · ⚠️ 1",
		"Вера · 4 — ✅ 2 · 🕓 2",
		"ср 21.10 · 1 — 🔕 1",
		"без исполнителя → основной админ · 3 — ⚠️ 1 · 🕓 1 · 🔕 1",
		"📎 <b>Материал · 4</b> (посты и рилсы)",
		"@smm_lead — чт ⚠️ · пт ✅",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if len(got) > 4096 {
		t.Errorf("week report is %d bytes, more than a Telegram message", len(got))
	}
	if empty := weekText(nil, nil, nil, now, true); !strings.Contains(empty, "Публикаций на этой неделе нет") ||
		!strings.Contains(empty, "Опросов на этой неделе не было") {
		t.Errorf("empty week:\n%s", empty)
	}
}

func TestStartOfWeek(t *testing.T) {
	for _, day := range []time.Time{at(19, 0, 0), at(22, 12, 0), at(25, 23, 59)} {
		if got := startOfWeek(day); !got.Equal(at(19, 0, 0)) {
			t.Errorf("startOfWeek(%s) = %s", day, got)
		}
	}
}

func TestPublishDataFitsTelegram(t *testing.T) {
	task := storage.Task{ID: "c8e6813f-5519-4bc4-8080-82388a0c4f20", PublishAt: at(26, 11, 0)}
	if d := publishData(task, "y"); len(d) > 64 {
		t.Errorf("callback data %q is %d bytes, Telegram allows 64", d, len(d))
	}
}
