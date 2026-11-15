package telegram

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-telegram/bot"
)

func TestIsTransient(t *testing.T) {
	for err, want := range map[error]bool{
		errors.New("error response from telegram for method getUpdates, 502 Bad Gateway"):                  true,
		errors.New("error response from telegram for method sendMessage, 500 Internal Server Error"):       true,
		fmt.Errorf("error do request for method sendMessage, %w", errors.New("context deadline exceeded")): true,
		&bot.TooManyRequestsError{Message: "too many requests", RetryAfter: 5}:                             true,
		fmt.Errorf("error response from telegram for method sendMessage, %w", bot.ErrorForbidden):          false,
		errors.New("error response from telegram for method sendMessage, 400 Bad Request: chat not found"): false,
		fmt.Errorf("%w, Bad Request: can't parse entities", bot.ErrorBadRequest):                           false,
	} {
		if got := isTransient(err); got != want {
			t.Errorf("isTransient(%q) = %v, want %v", err, got, want)
		}
	}
	if isTransient(nil) {
		t.Error("nil is not an error")
	}
}

func TestRetryPause(t *testing.T) {
	if got := retryPause(&bot.TooManyRequestsError{RetryAfter: 7}, 1); got != 7*time.Second {
		t.Errorf("retry_after 7: %s", got)
	}
	if got := retryPause(&bot.TooManyRequestsError{RetryAfter: 600}, 1); got != maxRetryAfter {
		t.Errorf("retry_after 600: %s, want the cap", got)
	}
	if got := retryPause(errors.New("502"), 2); got != 2*sendRetryPause {
		t.Errorf("attempt 2: %s", got)
	}
}

func TestAlertsDue(t *testing.T) {
	var a alerts
	now := time.Now()
	if !a.due("send", now) || a.due("send", now.Add(time.Hour)) {
		t.Error("the same alert within alertEvery must be sent once")
	}
	if !a.due("sync", now) {
		t.Error("another kind of alert is separate")
	}
	if !a.due("send", now.Add(alertEvery)) {
		t.Error("after alertEvery the alert goes again")
	}
}

func TestSyncStale(t *testing.T) {
	msk, _ := time.LoadLocation("Europe/Moscow")
	at := func(day, h, m int) time.Time { return time.Date(2026, 11, day, h, m, 0, 0, msk) }
	started := at(10, 12, 0)
	for _, tc := range []struct {
		name string
		now  time.Time
		last time.Time
		want bool
	}{
		{"just synced", at(14, 12, 0), at(14, 11, 35), false},
		{"1h59m without a sync", at(14, 13, 34), at(14, 11, 35), false},
		{"2h without a sync", at(14, 13, 35), at(14, 11, 35), true},
		{"night: never stale", at(14, 3, 0), at(13, 22, 35), false},
		{"morning after the night pause", at(14, 6, 0), at(13, 22, 35), false},
		{"morning, nothing since 05:00 for 2h", at(14, 7, 0), at(13, 22, 35), true},
		{"no sync since a recent start", at(10, 13, 0), time.Time{}, false},
	} {
		var last int64
		if !tc.last.IsZero() {
			last = tc.last.Unix()
		}
		if got := syncStale(tc.now, started, last); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsUnreachableUser(t *testing.T) {
	// The errors as go-telegram/bot builds them (raw_request.go).
	forbidden := func(d string) error { return fmt.Errorf("%w, %s", bot.ErrorForbidden, d) }
	badRequest := func(d string) error { return fmt.Errorf("%w, %s", bot.ErrorBadRequest, d) }
	for _, tc := range []struct {
		chatID int64
		err    error
		want   bool
	}{
		{42, badRequest("Bad Request: chat not found"), true},
		{42, forbidden("Forbidden: bot was blocked by the user"), true},
		{42, forbidden("Forbidden: bot can't initiate conversation with a user"), true},
		{42, forbidden("Forbidden: user is deactivated"), true},
		{-100123, badRequest("Bad Request: chat not found"), false}, // a group is a real problem
		{-100123, forbidden("Forbidden: bot was kicked from the supergroup chat"), false},
		{42, badRequest("Bad Request: can't parse entities"), false},
		{42, errors.New("error response from telegram for method sendMessage, 502 Bad Gateway"), false},
		{42, errors.New("Bad Request: chat not found"), false}, // not from the library
		{42, nil, false},
	} {
		if got := isUnreachableUser(tc.chatID, tc.err); got != tc.want {
			t.Errorf("isUnreachableUser(%d, %v) = %v, want %v", tc.chatID, tc.err, got, tc.want)
		}
	}
}
