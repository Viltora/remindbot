// Package config loads settings from environment variables.
//
// Secrets are read from files: for a secret NAME the variable NAME_FILE holds
// the path to a file with the value. NAME itself is accepted as a fallback.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // timezones work in minimal containers without tzdata
)

type Config struct {
	TelegramToken string
	// SuperadminID is the Telegram user ID of the bot owner. 0 means not set yet:
	// the bot still starts so the owner can learn their ID with /whoami.
	SuperadminID int64
	// TestRecipientIDs turn on test mode: every scheduled notification goes
	// to these users only, each getting a copy. Empty means off.
	TestRecipientIDs []int64
	// YouGileAPIKey reads tasks from YouGile. "" turns the integration off.
	YouGileAPIKey string
	Timezone      *time.Location
	HealthAddr    string
	LogLevel      slog.Level
	// SalesReportURL is the sales report in Yandex Documents; when set, the
	// reminder to fill it in gets a button with the link.
	SalesReportURL string
	// LogFile, if set, gets a copy of the logs (appended).
	LogFile string
	DB      DB
	// Features switch scenarios on; each is off unless set to true.
	Features Features
}

// Features are the scenarios that can be switched off in .env. Reminders
// about publications and the material requests are always on.
type Features struct {
	// SalesPoll is the evening sales report poll (FEATURE_SALES_POLL).
	SalesPoll bool
	// PublishQuestions is "published?" after a publication (FEATURE_PUBLISH_QUESTIONS).
	PublishQuestions bool
}

// Redactor cuts the secrets out of text, e.g. log lines: the Telegram library
// puts the bot token into request URLs, and so into its errors.
func (c Config) Redactor() *strings.Replacer {
	var pairs []string
	for secret, name := range map[string]string{
		c.TelegramToken: "<telegram-token>",
		c.YouGileAPIKey: "<yougile-key>",
		c.DB.Password:   "<db-password>",
	} {
		if secret != "" {
			pairs = append(pairs, secret, name)
		}
	}
	return strings.NewReplacer(pairs...)
}

type DB struct {
	Host     string
	Port     string
	Name     string
	User     string
	Password string
}

// ConnString returns a libpq-style connection string without the password,
// so it is safe to log. The password is set on the pool config separately.
func (d DB) ConnString() string {
	return fmt.Sprintf("host=%s port=%s dbname=%s user=%s", d.Host, d.Port, d.Name, d.User)
}

func Load() (Config, error) {
	var errs []error
	cfg := Config{
		HealthAddr: env("HEALTH_ADDR", "127.0.0.1:8081"),
		DB: DB{
			Host: env("DATABASE_HOST", "localhost"),
			Port: env("DATABASE_PORT", "5432"),
			Name: env("DATABASE_NAME", "remindbot"),
			User: env("DATABASE_USER", "bot"),
		},
	}

	var err error
	if cfg.TelegramToken, err = secret("TELEGRAM_TOKEN"); err != nil {
		errs = append(errs, err)
	}
	if cfg.DB.Password, err = secret("POSTGRES_PASSWORD"); err != nil {
		errs = append(errs, err)
	}
	if cfg.YouGileAPIKey, err = optionalSecret("YOUGILE_API_KEY"); err != nil {
		errs = append(errs, err)
	}

	if cfg.SuperadminID, err = tgID("SUPERADMIN_TG_ID"); err != nil {
		errs = append(errs, err)
	}
	if cfg.TestRecipientIDs, err = tgIDList("TEST_RECIPIENT_TG_ID"); err != nil {
		errs = append(errs, err)
	}

	for name, dst := range map[string]*bool{
		"FEATURE_SALES_POLL":        &cfg.Features.SalesPoll,
		"FEATURE_PUBLISH_QUESTIONS": &cfg.Features.PublishQuestions,
	} {
		if *dst, err = flag(name); err != nil {
			errs = append(errs, err)
		}
	}

	tz := env("BOT_TIMEZONE", "Europe/Moscow")
	if cfg.Timezone, err = time.LoadLocation(tz); err != nil {
		errs = append(errs, fmt.Errorf("BOT_TIMEZONE: неизвестный часовой пояс %q", tz))
	}

	cfg.LogFile = os.Getenv("LOG_FILE")
	cfg.SalesReportURL = strings.TrimSpace(os.Getenv("SALES_REPORT_URL"))
	if u := cfg.SalesReportURL; u != "" && !strings.HasPrefix(u, "https://") {
		errs = append(errs, fmt.Errorf("SALES_REPORT_URL: нужна ссылка, начинающаяся с https://, получено %q", u))
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(env("LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}

	return cfg, errors.Join(errs...)
}

// tgID reads a Telegram user ID; an unset variable gives 0.
func tgID(name string) (int64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		return 0, fmt.Errorf("%s: ожидается число, получено %q", name, raw)
	}
	return id, nil
}

// tgIDList reads Telegram user IDs separated by commas: "1,2". An unset
// variable gives none.
func tgIDList(name string) ([]int64, error) {
	var ids []int64
	for _, part := range strings.Split(os.Getenv(name), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%s: ожидаются числа через запятую, получено %q", name, os.Getenv(name))
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// flag reads an on/off setting; an unset variable gives off.
func flag(name string) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: ожидается true или false, получено %q", name, raw)
	}
	return v, nil
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// optionalSecret is secret for a setting that may be left out: then it is "".
func optionalSecret(name string) (string, error) {
	if os.Getenv(name+"_FILE") == "" && os.Getenv(name) == "" {
		return "", nil
	}
	return secret(name)
}

func secret(name string) (string, error) {
	if path := os.Getenv(name + "_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: не удалось прочитать файл: %w", name, err)
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "", fmt.Errorf("%s_FILE: файл %s пустой", name, path)
		}
		return v, nil
	}
	if v := os.Getenv(name); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("не задан %s_FILE (путь к файлу с секретом)", name)
}
