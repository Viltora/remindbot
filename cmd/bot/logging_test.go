package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"remindbot/internal/config"
)

// No secret may reach the logs, however it is logged.
func TestLoggerRedactsSecrets(t *testing.T) {
	const token, key = "123456:AAH-secret-token", "yougile-secret-key"
	path := filepath.Join(t.TempDir(), "logs", "bot.log")
	cfg := config.Config{TelegramToken: token, YouGileAPIKey: key, LogFile: path, LogLevel: slog.LevelDebug}

	log, closeLog := newLogger(cfg)
	log.Error("send message", "err", errors.New(`Post "https://api.telegram.org/bot`+token+`/sendMessage": timeout`))
	log.Info("message with "+token+" inside", "url", "https://x/bot"+token, "list", []string{key})
	log.With("key", key).Debug("with attrs", slog.Group("g", "token", token))
	closeLog()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if strings.Contains(out, token) || strings.Contains(out, key) {
		t.Fatalf("a secret leaked into the log:\n%s", out)
	}
	if !strings.Contains(out, "<telegram-token>") || !strings.Contains(out, "<yougile-key>") {
		t.Errorf("secrets were not replaced by their names:\n%s", out)
	}
	if strings.Count(out, "\n") != 3 {
		t.Errorf("want 3 lines in the log file:\n%s", out)
	}
}

func TestRedactorWithoutSecrets(t *testing.T) {
	// An empty secret must not match everywhere.
	if got := (config.Config{}).Redactor().Replace("hello"); got != "hello" {
		t.Errorf("got %q", got)
	}
}
