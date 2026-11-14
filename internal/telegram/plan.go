package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"

	"github.com/riverqueue/river"
	"github.com/robfig/cron/v3"

	"remindbot/internal/storage"
)

// Weekly content plan reminder, in the content chats:
//
//	Monday 13:00  ask the team to fill in the plan; the admin is not tagged
const planFillCron = "0 13 * * 1"

type planFillArgs struct{}

func (planFillArgs) Kind() string { return "plan_fill" }

// planReviewArgs was Monday 18:00 "admin, approve the plan". It is no longer
// scheduled; the worker stays so a job left in the queue finishes quietly.
type planReviewArgs struct{}

func (planReviewArgs) Kind() string { return "plan_review" }

func (t *Bot) planJobs(workers *river.Workers) ([]*river.PeriodicJob, error) {
	river.AddWorker(workers, river.WorkFunc(func(ctx context.Context, _ *river.Job[planFillArgs]) error {
		return t.sendToGroups(ctx, "🗓 Напоминание: заполните, пожалуйста, контент-план на неделю в YouGile.")
	}))
	river.AddWorker(workers, river.WorkFunc(func(context.Context, *river.Job[planReviewArgs]) error { return nil }))

	schedule, err := cron.ParseStandard("CRON_TZ=" + t.tz.String() + " " + planFillCron)
	if err != nil {
		return nil, fmt.Errorf("plan_fill schedule: %w", err)
	}
	return []*river.PeriodicJob{river.NewPeriodicJob(schedule,
		func() (river.JobArgs, *river.InsertOpts) { return planFillArgs{}, nil },
		&river.PeriodicJobOpts{ID: "plan_fill"})}, nil
}

// adminToTag returns the mention of the admin the bot addresses: the primary
// admin, or all admins if none is marked primary, or "" if there are none.
func (t *Bot) adminToTag(ctx context.Context) (string, error) {
	primary, err := t.store.PrimaryAdmin(ctx)
	if err == nil {
		return mention(primary), nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return "", err
	}
	admins, err := t.store.ListUsers(ctx, storage.RoleAdmin, 1000)
	if err != nil {
		return "", err
	}
	names := make([]string, len(admins))
	for i, a := range admins {
		names[i] = mention(a)
	}
	return strings.Join(names, ", "), nil
}

// sendToGroups posts an HTML message to every content chat, or in test mode
// to the test recipient.
func (t *Bot) sendToGroups(ctx context.Context, text string) error {
	chats, err := t.contentChats(ctx)
	if err != nil {
		return err
	}
	if len(chats) == 0 {
		t.log.Warn("no content chats to send to; assign one in /chats", "text", preview(text))
	}
	t.log.Info("content chats message", "chats", len(chats), "text", preview(text))
	for _, id := range chats {
		t.notify(ctx, id, text, nil)
	}
	return nil
}

// mention tags a user in a chat: by @username, or by a link to their ID when
// they have no username (works for users who have started the bot).
func mention(u storage.User) string {
	if u.Username != "" {
		return "@" + html.EscapeString(u.Username)
	}
	return fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, u.TgID, html.EscapeString(u.FirstName))
}
