package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"remindbot/internal/config"
)

// newLogger logs to stdout and, with LOG_FILE, appends a copy to that file,
// rotated by size (logrotate.go); closeLog closes it. Secrets are cut out of every log line, whoever writes it.
func newLogger(cfg config.Config) (log *slog.Logger, closeLog func()) {
	var w io.Writer = os.Stdout
	closeLog = func() {}
	var fileErr error
	if cfg.LogFile != "" {
		f, err := openRotatingFile(cfg.LogFile, logMaxSize, logKeep)
		if err == nil {
			w, closeLog = io.MultiWriter(os.Stdout, f), func() { f.Close() }
		}
		fileErr = err
	}

	redactor := cfg.Redactor()
	log = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: cfg.LogLevel,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			return redactAttr(a, redactor)
		},
	}))
	// A log file is a convenience: without it the bot still runs.
	if fileErr != nil {
		log.Warn("cannot open LOG_FILE, logging to stdout only", "path", cfg.LogFile, "err", fileErr)
	}
	return log, closeLog
}

// redactAttr replaces secrets in a log attribute, the message included.
func redactAttr(a slog.Attr, r *strings.Replacer) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(r.Replace(v.String()))
	case slog.KindAny:
		s := fmt.Sprint(v.Any())
		if err, ok := v.Any().(error); ok {
			s = err.Error()
		}
		if clean := r.Replace(s); clean != s {
			a.Value = slog.StringValue(clean)
		}
	}
	return a
}
