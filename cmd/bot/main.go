package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"remindbot/internal/config"
	"remindbot/internal/health"
	"remindbot/internal/storage"
	"remindbot/internal/telegram"
	"remindbot/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

func run() error {
	// .env is for local runs; in a container the variables come from compose.
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf(".env: %w", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("конфигурация:\n%w", err)
	}

	log, closeLog := newLogger(cfg)
	defer closeLog()
	log.Info("starting remindbot", "version", version.Version,
		"timezone", cfg.Timezone.String(), "superadmin", cfg.SuperadminID,
		"test_recipients", cfg.TestRecipientIDs, "yougile", cfg.YouGileAPIKey != "",
		"log_level", cfg.LogLevel.String(), "log_file", cfg.LogFile,
		"sales_poll", cfg.Features.SalesPoll,
		"publish_questions", cfg.Features.PublishQuestions)
	if cfg.SuperadminID == 0 {
		log.Warn("SUPERADMIN_TG_ID is not set: send /whoami to the bot and put your ID into .env")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.Open(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer store.Close()

	if err := store.Migrate(ctx, log); err != nil {
		return err
	}

	tg, err := telegram.New(ctx, cfg, store, log)
	if err != nil {
		return err
	}

	jobs, err := startJobs(ctx, store, tg, log)
	if err != nil {
		return err
	}

	go health.Serve(ctx, cfg.HealthAddr, store.Ping, log)

	log.Info("bot is running", "username", "@"+tg.Username())
	tg.Start(ctx) // blocks until SIGINT/SIGTERM

	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := jobs.Stop(stopCtx); err != nil {
		log.Warn("job queue did not stop cleanly", "err", err)
	}
	log.Info("bot stopped")
	return nil
}

// startJobs runs the River job queue: scheduled reminders and their follow-ups.
func startJobs(ctx context.Context, store *storage.Store, tg *telegram.Bot, log *slog.Logger) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	periodic, err := tg.RegisterJobs(workers)
	if err != nil {
		return nil, err
	}
	client, err := river.NewClient(riverpgxv5.New(store.Pool()), &river.Config{
		Logger:       log.With("component", "river"),
		Queues:       map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 5}},
		Workers:      workers,
		PeriodicJobs: periodic,
	})
	if err != nil {
		return nil, fmt.Errorf("init job queue: %w", err)
	}
	// Stopped explicitly after the bot, so in-flight jobs can finish.
	if err := client.Start(context.WithoutCancel(ctx)); err != nil {
		return nil, fmt.Errorf("start job queue: %w", err)
	}
	return client, nil
}
