package telegram

import (
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

// The sales reminder goes on Monday, Wednesday and Friday at 16:00, the
// content plan reminder on Monday at 13:00, in the bot's time zone.
func TestReminderCrons(t *testing.T) {
	msk, _ := time.LoadLocation("Europe/Moscow")
	next := func(spec string, from time.Time, n int) []string {
		s, err := cron.ParseStandard("CRON_TZ=Europe/Moscow " + spec)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for range n {
			from = s.Next(from)
			out = append(out, from.In(msk).Format("Mon 15:04"))
		}
		return out
	}
	sun := time.Date(2026, 11, 15, 12, 0, 0, 0, msk)
	if got, want := next(salesRemindCron, sun, 4), []string{"Mon 16:00", "Wed 16:00", "Fri 16:00", "Mon 16:00"}; !equal(got, want) {
		t.Errorf("sales reminder: %v, want %v", got, want)
	}
	if got, want := next(planFillCron, sun, 2), []string{"Mon 13:00", "Mon 13:00"}; !equal(got, want) {
		t.Errorf("content plan: %v, want %v", got, want)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
