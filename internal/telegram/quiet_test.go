package telegram

import (
	"testing"
	"time"
)

func TestOutsideQuietHours(t *testing.T) {
	msk, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	at := func(day, hour, min int) time.Time { return time.Date(2026, 10, day, hour, min, 0, 0, msk) }

	for _, tc := range []struct{ in, want time.Time }{
		{at(20, 18, 40), at(20, 18, 40)}, // daytime: unchanged
		{at(20, 21, 59), at(20, 21, 59)},
		{at(20, 22, 0), at(21, 6, 0)},  // evening → next morning
		{at(20, 23, 39), at(21, 6, 0)}, // evening → next morning
		{at(21, 3, 15), at(21, 6, 0)},  // night → same morning
		{at(21, 6, 0), at(21, 6, 0)},
	} {
		if got := outsideQuietHours(tc.in); !got.Equal(tc.want) {
			t.Errorf("outsideQuietHours(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}
