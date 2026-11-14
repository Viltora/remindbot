package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Sending: a temporary Telegram failure (5xx, network, timeout, 429) is retried
// a few times with a pause. A message that still did not go is logged as an
// error and the superadmin is told, at most once per alertEvery for the same
// kind of trouble.
const (
	sendAttempts   = 3
	sendRetryPause = 3 * time.Second
	maxRetryAfter  = 30 * time.Second
	alertEvery     = 3 * time.Hour
	// syncStaleAfter without a successful YouGile sync, during the hours the
	// bot syncs, the superadmin is told.
	syncStaleAfter = 2 * time.Hour
)

var telegramStatus = regexp.MustCompile(`error response from telegram for method \w+, (\d{3})`)

// isTransient reports whether a Telegram API error is likely to pass by
// itself: a server error, a network failure or a timeout, or a rate limit.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	if bot.IsTooManyRequestsError(err) || errors.Is(err, bot.ErrorTooManyRequests) {
		return true
	}
	if m := telegramStatus.FindStringSubmatch(err.Error()); m != nil {
		code, _ := strconv.Atoi(m[1])
		return code >= 500
	}
	// The library wraps transport errors (timeouts, resets, DNS) this way.
	return regexpDoRequest.MatchString(err.Error())
}

var regexpDoRequest = regexp.MustCompile(`^error do request for method `)

// retryPause is how long to wait before the next attempt: Telegram's
// retry_after when it gives one, else a growing pause.
func retryPause(err error, attempt int) time.Duration {
	var tm *bot.TooManyRequestsError
	if errors.As(err, &tm) && tm.RetryAfter > 0 {
		return min(time.Duration(tm.RetryAfter)*time.Second, maxRetryAfter)
	}
	return time.Duration(attempt) * sendRetryPause
}

// sendWithRetry sends a message, retrying temporary failures.
func (t *Bot) sendWithRetry(ctx context.Context, params *bot.SendMessageParams) (*models.Message, error) {
	var err error
	for attempt := 1; ; attempt++ {
		var m *models.Message
		m, err = t.api.SendMessage(ctx, params)
		if err == nil {
			return m, nil
		}
		if attempt >= sendAttempts || !isTransient(err) {
			return nil, err
		}
		pause := retryPause(err, attempt)
		t.log.Warn("send message failed, retrying", "chat_id", params.ChatID, "attempt", attempt, "in", pause, "err", err)
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(pause):
		}
	}
}

// alerts keeps when each kind of alert was last sent, so the superadmin is
// not flooded.
type alerts struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (a *alerts) due(key string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.last == nil {
		a.last = map[string]time.Time{}
	}
	if at, ok := a.last[key]; ok && now.Sub(at) < alertEvery {
		return false
	}
	a.last[key] = now
	return true
}

// alert tells the superadmin about trouble in a direct message, once per
// alertEvery for the same key. It is sent past test mode and without retries
// or further alerts, so a failing Telegram cannot loop.
func (t *Bot) alert(ctx context.Context, key, text string) (sent bool) {
	if t.superadminID == 0 || !t.alerts.due(key, time.Now()) {
		return false
	}
	t.log.Warn("alert to the superadmin", "key", key, "text", preview(text))
	_, err := t.api.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: t.superadminID, Text: "⚠️ <b>remindbot</b>\n" + text, ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		t.log.Error("alert not sent", "key", key, "err", err)
		return false
	}
	return true
}

// alertSendFailed tells the superadmin that a message did not go.
func (t *Bot) alertSendFailed(ctx context.Context, chatID int64, text string, err error) {
	if chatID == t.superadminID {
		return // the alert would go to the same chat
	}
	t.alert(ctx, "send", fmt.Sprintf(
		"Не удалось отправить сообщение в чат <code>%d</code> после %d попыток.\n"+
			"Ошибка: <code>%s</code>\nСообщение: %s\n\n"+
			"Если это напоминание о задаче, бот попробует снова через минуту. "+
			"Похожие сбои в ближайшие 3 часа — только в логе.",
		chatID, sendAttempts, html.EscapeString(shorten(err.Error(), 200)), html.EscapeString(preview(text))))
}

// checkSyncStale tells the superadmin when YouGile has not synced for
// syncStaleAfter during the sync hours, and once when it works again.
func (t *Bot) checkSyncStale(ctx context.Context, now time.Time) {
	if t.yg == nil || !syncStale(now, t.startedAt, t.lastSync.Load()) {
		return
	}
	last := "ни одной с запуска бота"
	if ts := t.lastSync.Load(); ts != 0 {
		last = time.Unix(ts, 0).In(t.tz).Format("02.01 15:04")
	}
	text := fmt.Sprintf("Сверка с YouGile не проходит больше %d ч. Последняя успешная: %s.\n"+
		"Напоминания идут по старым данным: новые и перенесённые задачи бот не видит.",
		int(syncStaleAfter.Hours()), last)
	if e, _ := t.lastSyncErr.Load().(string); e != "" {
		text += "\nПоследняя ошибка: <code>" + html.EscapeString(shorten(e, 200)) + "</code>"
	}
	if t.alert(ctx, "sync", text) {
		t.syncAlerted.Store(true)
	}
}

// syncRecovered tells the superadmin, once, that YouGile syncs again after
// an alert.
func (t *Bot) syncRecovered(ctx context.Context) {
	if t.syncAlerted.Swap(false) && t.superadminID != 0 {
		t.send(ctx, t.superadminID, "✅ <b>remindbot</b>\nСверка с YouGile снова работает.", nil)
	}
}

// syncStale reports whether YouGile has gone unsynced for syncStaleAfter:
// counted from the last successful sync (Unix time, 0 = none), the start of
// the bot or the first sync hour of the day, whichever is latest — the
// night pause is not staleness. At night it is never stale.
func syncStale(now, startedAt time.Time, lastSync int64) bool {
	if now.Hour() < yougileSyncFromHour || now.Hour() >= yougileSyncToHour {
		return false
	}
	ref := startedAt
	if lastSync != 0 && time.Unix(lastSync, 0).After(ref) {
		ref = time.Unix(lastSync, 0)
	}
	if morning := time.Date(now.Year(), now.Month(), now.Day(), yougileSyncFromHour, 0, 0, 0, now.Location()); morning.After(ref) {
		ref = morning
	}
	return now.Sub(ref) >= syncStaleAfter
}

func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
