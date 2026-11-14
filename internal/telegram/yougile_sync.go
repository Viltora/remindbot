package telegram

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/riverqueue/river"
	"github.com/robfig/cron/v3"

	"remindbot/internal/storage"
)

// Tasks are read from YouGile every 30 minutes by day; at night the bot does
// not call YouGile. Quiet hours end at 07:00, so the 06:05 and 06:35 syncs
// are in time for the morning.
const (
	yougileSyncCron     = "5,35 6-22 * * *"
	yougileSyncFromHour = 6
	yougileSyncToHour   = 23
	// The copy keeps tasks published within this window around now.
	syncPast  = 24 * time.Hour
	syncAhead = 8 * 24 * time.Hour
	// A sync may wait out YouGile's rate limit (up to a minute per retry), so
	// it gets more than River's default one-minute job timeout.
	yougileSyncTimeout = 10 * time.Minute
	// A failed sync is retried a couple of times soon after; otherwise the next
	// one comes in 30 minutes, without spending the shared request limit.
	yougileSyncAttempts = 3
)

type yougileSyncArgs struct{}

func (yougileSyncArgs) Kind() string { return "yougile_sync" }

type yougileSyncWorker struct {
	river.WorkerDefaults[yougileSyncArgs]
	t *Bot
}

func (w *yougileSyncWorker) Timeout(*river.Job[yougileSyncArgs]) time.Duration {
	return yougileSyncTimeout
}

func (w *yougileSyncWorker) Work(ctx context.Context, job *river.Job[yougileSyncArgs]) error {
	return w.t.workYouGileSync(ctx, job)
}

func (t *Bot) yougileSyncJob(workers *river.Workers) (*river.PeriodicJob, error) {
	river.AddWorker(workers, &yougileSyncWorker{t: t})
	schedule, err := cron.ParseStandard("CRON_TZ=" + t.tz.String() + " " + yougileSyncCron)
	if err != nil {
		return nil, fmt.Errorf("yougile sync schedule: %w", err)
	}
	return river.NewPeriodicJob(schedule,
		func() (river.JobArgs, *river.InsertOpts) {
			return yougileSyncArgs{}, &river.InsertOpts{MaxAttempts: yougileSyncAttempts}
		},
		&river.PeriodicJobOpts{ID: "yougile_sync", RunOnStart: true}), nil
}

func (t *Bot) workYouGileSync(ctx context.Context, _ *river.Job[yougileSyncArgs]) error {
	now := time.Now().In(t.tz)
	if h := now.Hour(); h < yougileSyncFromHour || h >= yougileSyncToHour {
		t.log.Info("yougile sync skipped at night", "next_at", fmt.Sprintf("%02d:05", yougileSyncFromHour))
		return nil // RunOnStart at night
	}
	if err := t.syncYouGile(ctx, now); err != nil {
		t.lastSyncErr.Store(err.Error())
		return err
	}
	t.lastSyncErr.Store("")
	t.syncRecovered(ctx)
	return nil
}

// syncYouGile copies the tasks of the tracked columns from YouGile. Any
// failed request fails the whole sync, so tasks are never deactivated on
// partial data; River retries it.
func (t *Bot) syncYouGile(ctx context.Context, now time.Time) error {
	columns, err := t.store.TaskColumns(ctx)
	if err != nil {
		return err
	}
	ygUsers, err := t.yg.Users(ctx)
	if err != nil {
		return err
	}
	users := make([]storage.YouGileUser, len(ygUsers))
	for i, u := range ygUsers {
		users[i] = storage.YouGileUser{ID: u.ID, RealName: u.RealName, Email: u.Email}
	}

	from, to := now.Add(-syncPast), now.Add(syncAhead)
	before, err := t.store.ActiveTasks(ctx, from, to)
	if err != nil {
		return err
	}
	var tasks []storage.Task
	for _, c := range columns {
		ygTasks, err := t.yg.Tasks(ctx, c.ColumnID)
		if err != nil {
			return err
		}
		t.log.Info("yougile column read", "column", c.Title, "type", c.Type, "tasks", len(ygTasks))
		for _, yt := range ygTasks {
			publishAt, ok := yt.Start()
			if yt.Archived || yt.IsDeleted() || !ok || publishAt.Before(from) || !publishAt.Before(to) {
				continue
			}
			tasks = append(tasks, storage.Task{
				ID:        yt.ID,
				Type:      c.Type,
				Title:     strings.TrimSpace(yt.Title),
				PublishAt: publishAt,
				Assignees: yt.Assigned,
			})
		}
	}

	if err := t.store.SyncTasks(ctx, users, tasks, from, to); err != nil {
		return err
	}
	t.lastSync.Store(now.Unix())
	changes := t.logTaskChanges(before, tasks, users)
	t.log.Info("yougile synced", "tasks_in_window", len(tasks), "changes", changes, "users", len(users),
		"took", time.Since(now).Round(time.Millisecond))
	return nil
}

// logTaskChanges logs how the copy of the tasks changed in this sync and
// returns the number of changes.
func (t *Bot) logTaskChanges(before, after []storage.Task, users []storage.YouGileUser) int {
	names := map[string]string{}
	for _, u := range users {
		names[u.ID] = u.RealName
	}
	who := func(ids []string) string {
		var out []string
		for _, id := range ids {
			out = append(out, names[id])
		}
		return strings.Join(out, ", ")
	}
	at := func(task storage.Task) string { return task.PublishAt.In(t.tz).Format(time.DateTime) }

	old := make(map[string]storage.Task, len(before))
	for _, task := range before {
		old[task.ID] = task
	}
	changes := 0
	for _, task := range after {
		prev, ok := old[task.ID]
		delete(old, task.ID)
		attrs := []any{"task", task.ID, "type", task.Type, "title", task.Title, "publish_at", at(task), "assignees", who(task.Assignees)}
		switch {
		case !ok:
			if task.Type.NeedsMaterial() {
				attrs = append(attrs, "material_request_at", materialRequestTime(task.PublishAt.In(t.tz)).Format(time.DateTime))
			}
			t.log.Info("task appeared", attrs...)
		case !prev.PublishAt.Equal(task.PublishAt):
			t.log.Info("task moved", append(attrs, "was", at(prev))...)
		case prev.Title != task.Title || prev.Type != task.Type || !slices.Equal(prev.Assignees, task.Assignees):
			t.log.Info("task changed", append(attrs, "was_title", prev.Title, "was_type", prev.Type, "was_assignees", who(prev.Assignees))...)
		default:
			continue
		}
		changes++
	}
	for _, task := range old {
		t.log.Info("task gone: deleted, archived or moved out", "task", task.ID, "type", task.Type, "title", task.Title, "publish_at", at(task))
		changes++
	}
	return changes
}

// yougileStatus is the /status line about YouGile.
func (t *Bot) yougileStatus(ctx context.Context) string {
	if t.yg == nil {
		return "📋 YouGile: не подключён (нет YOUGILE_API_KEY_FILE)"
	}
	last := "ещё не было с запуска"
	if ts := t.lastSync.Load(); ts != 0 {
		last = time.Unix(ts, 0).In(t.tz).Format("15:04")
	}
	now := time.Now()
	n, err := t.store.CountActiveTasks(ctx, now, now.Add(7*24*time.Hour))
	if err != nil {
		t.log.Error("count tasks", "err", err)
		return "📋 YouGile: сверка " + last
	}
	return fmt.Sprintf("📋 YouGile: сверка %s · публикаций на 7 дней: %d", last, n)
}
