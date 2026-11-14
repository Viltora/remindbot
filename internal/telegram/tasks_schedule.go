package telegram

import (
	"time"

	"remindbot/internal/storage"
)

// Reminders about publications from YouGile, in the content chats. P is the
// publication time; every send is moved out of quiet hours.
//
// Material for approval (posts, reels):
//
//	P−24h       request; if that is a weekend, on Friday at the same time.
//	            The material is due a day ahead, so a request later than
//	            materialRequestGrace is not sent at all.
//	+1h         one reminder if there is no material; the admin is not told
//	            nothing is sent from P on
//
// Publishing (all types):
//
//	P−60m, P−30m  reminders: only the latest due one, only before P
//	P+30m         "published?" [Yes] [No], then every 30 minutes, 4 times
//	+30m          "no answer" is recorded, no message
const (
	materialLead      = 24 * time.Hour
	materialReminders = 1
	materialGap       = time.Hour
	// The last material step: the request is step 1, the reminders follow.
	materialLastStep = materialReminders + 1
	taskStep         = 30 * time.Minute
	// How late a material request may still go: the sync runs every 30
	// minutes, so a task added just before its request time is seen this late.
	materialRequestGrace = 30 * time.Minute
	publishAttempts      = 4
	// A first question more overdue than this is not asked: the bot was down
	// or has just learned about the task, and asking now is no use.
	askCatchUp = 4 * time.Hour
)

// publishReminders are how long before P the reminders go, in order.
var publishReminders = []time.Duration{time.Hour, 30 * time.Minute}

// materialRequestTime is when the material is asked for: a day before p, but
// not on a weekend — then on Friday, so it is ready before the weekend. If
// quiet hours would push a Friday evening request to Saturday, it goes on
// Friday morning instead.
func materialRequestTime(p time.Time) time.Time {
	r := outsideQuietHours(toFriday(p.Add(-materialLead)))
	if f := toFriday(r); !f.Equal(r) {
		r = time.Date(f.Year(), f.Month(), f.Day(), quietToHour, 0, 0, 0, f.Location())
	}
	return r
}

// toFriday moves a Saturday or Sunday back to Friday, keeping the time.
func toFriday(t time.Time) time.Time {
	switch t.Weekday() {
	case time.Saturday:
		return t.AddDate(0, 0, -1)
	case time.Sunday:
		return t.AddDate(0, 0, -2)
	}
	return t
}

// materialDue returns the material step due at now, if any: 1 request,
// then the reminders up to materialLastStep. Times are taken in
// now's location, the bot's time zone.
func materialDue(task storage.Task, now time.Time) (step int, due bool) {
	p := task.PublishAt.In(now.Location())
	if !task.Type.NeedsMaterial() || !task.MaterialReceivedAt.IsZero() ||
		task.MaterialStep >= materialLastStep || !now.Before(p) {
		return 0, false
	}
	if task.MaterialStep == 0 {
		at := materialRequestTime(p)
		return 1, !now.Before(at) && now.Sub(at) <= materialRequestGrace
	}
	at := outsideQuietHours(task.MaterialStepAt.In(now.Location()).Truncate(time.Minute).Add(materialGap))
	return task.MaterialStep + 1, !now.Before(at)
}

// materialRequestMissed reports whether the time to ask for the material
// passed without a request: then there is none.
func materialRequestMissed(task storage.Task, now time.Time) bool {
	p := task.PublishAt.In(now.Location())
	return task.Type.NeedsMaterial() && task.MaterialStep == 0 && task.MaterialReceivedAt.IsZero() &&
		now.Before(p) && now.Sub(materialRequestTime(p)) > materialRequestGrace
}

type publishAction int

const (
	publishNone publishAction = iota
	publishRemind
	publishAsk
	publishNoAnswer
)

// publishDue returns what is due at now about publishing the task. For
// publishRemind n is the reminder (1 an hour before, 2 half an hour before),
// for publishAsk the attempt.
func publishDue(task storage.Task, now time.Time) (act publishAction, n int) {
	if task.Status != storage.PublishPending {
		return publishNone, 0
	}
	loc := now.Location()
	p := task.PublishAt.In(loc)

	if now.Before(p) {
		for i := len(publishReminders); i > task.RemindersSent; i-- {
			at := outsideQuietHours(p.Add(-publishReminders[i-1]))
			if !now.Before(at) && at.Before(p) {
				return publishRemind, i
			}
		}
		return publishNone, 0
	}

	if task.Attempts == 0 {
		at := outsideQuietHours(p.Add(taskStep))
		if now.Before(at) || now.Sub(at) > askCatchUp {
			return publishNone, 0
		}
		return publishAsk, 1
	}
	// From the minute of the last question: the tick runs once a minute, and
	// counting from its seconds would delay each next step by a minute.
	if now.Before(outsideQuietHours(task.AskedAt.In(loc).Truncate(time.Minute).Add(taskStep))) {
		return publishNone, 0
	}
	if task.Attempts < publishAttempts {
		return publishAsk, task.Attempts + 1
	}
	return publishNoAnswer, 0
}
