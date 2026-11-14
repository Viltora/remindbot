// Package telegram wires the Telegram Bot API to the bot's handlers.
package telegram

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"remindbot/internal/config"
	"remindbot/internal/storage"
	"remindbot/internal/yougile"
)

// How long an in-flight handler may keep running after shutdown starts.
const handlerShutdownTimeout = 15 * time.Second

type Bot struct {
	api          *bot.Bot
	store        *storage.Store
	log          *slog.Logger
	username     string
	botID        int64
	superadminID int64
	// testRecipients get every scheduled notification in test mode; none = off.
	testRecipients []int64
	tz             *time.Location
	// features are the scenarios switched on in .env.
	features  config.Features
	startedAt time.Time
	// yg reads tasks from YouGile; nil when there is no API key.
	yg       *yougile.Client
	lastSync atomic.Int64 // Unix time of the last successful sync, 0 = none yet
	// lastSyncErr is the error of the last failed sync, for the alert.
	lastSyncErr atomic.Value
	// syncAlerted is set after a "sync is stale" alert, until it syncs again.
	syncAlerted atomic.Bool
	alerts      alerts
	// skipsLogged remembers the skipped task steps already logged.
	skipsLogged sync.Map

	// The library runs handlers asynchronously and Start does not wait for them,
	// so in-flight handlers are tracked here for graceful shutdown.
	inflight sync.WaitGroup
}

func New(ctx context.Context, cfg config.Config, store *storage.Store, log *slog.Logger) (*Bot, error) {
	// The token is part of every API URL, so transport errors may contain it.
	// The logger cuts it out too; errors returned from here are printed as is.
	redactor := cfg.Redactor()
	redact := func(err error) string { return redactor.Replace(err.Error()) }

	if err := checkTestRecipients(ctx, store, cfg.TestRecipientIDs, log); err != nil {
		return nil, err
	}

	t := &Bot{store: store, log: log, superadminID: cfg.SuperadminID, testRecipients: cfg.TestRecipientIDs,
		tz: cfg.Timezone, features: cfg.Features, startedAt: time.Now()}
	if cfg.YouGileAPIKey != "" {
		t.yg = yougile.New(cfg.YouGileAPIKey, log)
	} else {
		log.Warn("YOUGILE_API_KEY_FILE is not set: reminders about YouGile tasks are off")
	}
	api, err := bot.New(cfg.TelegramToken,
		bot.WithSkipGetMe(), // called below with our context
		bot.WithMiddlewares(t.trackInflight, t.logUpdate, t.observeGroups),
		bot.WithDefaultHandler(t.onUnhandled),
		// Telegram's own hiccups (502, timeouts) on receiving updates pass by
		// themselves: the library polls again. They are warnings, not errors.
		bot.WithErrorsHandler(func(err error) {
			if isTransient(err) {
				log.Warn("telegram api temporary error", "err", redact(err))
				return
			}
			log.Error("telegram api error", "err", redact(err))
		}),
	)
	if err != nil {
		return nil, errors.New(redact(err))
	}

	me, err := api.GetMe(ctx)
	if err != nil {
		return nil, errors.New("telegram getMe: " + redact(err))
	}
	t.api = api
	t.username = me.Username
	t.botID = me.ID

	api.RegisterHandlerMatchFunc(t.command("start"), t.onStart)
	api.RegisterHandlerMatchFunc(t.command("whoami"), t.onWhoami)

	api.RegisterHandlerMatchFunc(t.command("today"), t.adminOnly(t.onToday))
	api.RegisterHandlerMatchFunc(t.command("week"), t.adminOnly(t.onWeek))
	api.RegisterHandlerMatchFunc(t.command("month"), t.adminOnly(t.onMonth))
	api.RegisterHandlerMatchFunc(t.command("admins"), t.superadminOnly(t.onAdmins))
	api.RegisterHandlerMatchFunc(t.command("status"), t.superadminOnly(t.onStatus))
	api.RegisterHandlerMatchFunc(t.command("help"), t.superadminOnly(t.onHelp))
	api.RegisterHandlerMatchFunc(t.command("chats"), t.superadminOnly(t.onChats))
	api.RegisterHandler(bot.HandlerTypeCallbackQueryData, cbAdminPrefix, bot.MatchTypePrefix, t.onAdminCallback)
	api.RegisterHandler(bot.HandlerTypeCallbackQueryData, cbSalesPrefix, bot.MatchTypePrefix, t.onSalesAnswer)
	api.RegisterHandler(bot.HandlerTypeCallbackQueryData, cbChatPrefix, bot.MatchTypePrefix, t.onChatCallback)
	api.RegisterHandler(bot.HandlerTypeCallbackQueryData, cbPublishPrefix, bot.MatchTypePrefix, t.onPublishAnswer)
	api.RegisterHandler(bot.HandlerTypeCallbackQueryData, "noop", bot.MatchTypeExact, t.onNoop)
	// After the commands: a command sent as a reply is still a command.
	api.RegisterHandlerMatchFunc(t.isReplyToBot, t.onTaskReply)

	t.setupMenus(ctx)

	return t, nil
}

func (t *Bot) Username() string {
	return t.username
}

// Start receives updates via long polling until ctx is cancelled, then waits
// for in-flight handlers to finish.
func (t *Bot) Start(ctx context.Context) {
	t.api.Start(ctx)

	done := make(chan struct{})
	go func() {
		t.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(handlerShutdownTimeout):
		t.log.Warn("shutdown: some handlers did not finish in time")
	}
}

// trackInflight detaches handlers from the polling context: on shutdown that
// context is cancelled, and the handler would fail to send its reply.
func (t *Bot) trackInflight(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		t.inflight.Add(1)
		defer t.inflight.Done()

		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handlerShutdownTimeout)
		defer cancel()
		next(ctx, b, update)
	}
}

// command matches "/name" and "/name@thisbot" at the start of a message.
// In groups Telegram appends the bot username to commands.
func (t *Bot) command(name string) bot.MatchFunc {
	return func(update *models.Update) bool {
		msg := update.Message
		if msg == nil {
			return false
		}
		for _, e := range msg.Entities {
			if e.Type != models.MessageEntityTypeBotCommand || e.Offset != 0 {
				continue
			}
			// Commands are ASCII, so UTF-16 entity length equals byte length.
			if e.Length > len(msg.Text) {
				return false
			}
			cmd, target, _ := strings.Cut(msg.Text[1:e.Length], "@")
			if target != "" && !strings.EqualFold(target, t.username) {
				return false
			}
			return cmd == name
		}
		return false
	}
}

func (t *Bot) reply(ctx context.Context, msg *models.Message, html string) {
	t.send(ctx, msg.Chat.ID, html, nil)
}

// send sends an HTML message; markup may be nil. It returns the sent message,
// or nil if sending failed (the error is logged).
func (t *Bot) send(ctx context.Context, chatID int64, html string, markup models.ReplyMarkup) *models.Message {
	m, err := t.sendWithRetry(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        html,
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: markup,
	})
	if err != nil {
		t.log.Error("send message", "chat_id", chatID, "text", preview(html), "err", err)
		t.alertSendFailed(ctx, chatID, html, err)
		return nil
	}
	t.log.Info("sent", "chat_id", chatID, "message_id", m.ID, "text", preview(html))
	return m
}
