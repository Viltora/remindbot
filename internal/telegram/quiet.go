package telegram

import "time"

// Quiet hours: nothing scheduled is sent between 22:00 and 06:00 in the bot's
// time zone; such sends move to 06:00.
const (
	quietFromHour = 22
	quietToHour   = 6
)

func inQuietHours(t time.Time) bool {
	return !outsideQuietHours(t).Equal(t)
}

// outsideQuietHours returns t, or the end of the quiet period t falls into.
func outsideQuietHours(t time.Time) time.Time {
	morning := func(day time.Time) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), quietToHour, 0, 0, 0, day.Location())
	}
	switch h := t.Hour(); {
	case h >= quietFromHour:
		return morning(t.AddDate(0, 0, 1))
	case h < quietToHour:
		return morning(t)
	}
	return t
}
