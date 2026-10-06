// Package logging builds the controller's structured loggers.
//
// Each logical stream (app, audit, security, job) is a separate *slog.Logger.
// In file mode each stream writes to its own rotated file under
// <data_dir>/logs; in stdout mode all streams share stdout and are
// distinguished by the "stream" attribute.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/natefinch/lumberjack.v2"

	"drool/internal/config"
)

type Stream string

const (
	StreamApp      Stream = "app"
	StreamAudit    Stream = "audit"
	StreamSecurity Stream = "security"
	StreamJob      Stream = "job"
)

var allStreams = []Stream{StreamApp, StreamAudit, StreamSecurity, StreamJob}

// Loggers holds one logger per stream.
type Loggers struct {
	App      *slog.Logger
	Audit    *slog.Logger
	Security *slog.Logger
	Job      *slog.Logger

	closers []io.Closer
}

// Close flushes and closes any rotated log files.
func (l *Loggers) Close() error {
	var first error
	for _, c := range l.closers {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// New builds loggers from configuration. dataDir is used for file output.
func New(cfg config.Logging, dataDir string) (*Loggers, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		return nil, fmt.Errorf("logging.level: %w", err)
	}
	opts := &slog.HandlerOptions{Level: level, ReplaceAttr: Redact}

	l := &Loggers{}
	writers := map[Stream]io.Writer{}
	if cfg.Output == "stdout" {
		w := &syncWriter{w: os.Stdout}
		for _, s := range allStreams {
			writers[s] = w
		}
	} else {
		dir := filepath.Join(dataDir, "logs")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create log dir: %w", err)
		}
		for _, s := range allStreams {
			lj := &lumberjack.Logger{
				Filename:   filepath.Join(dir, string(s)+".log"),
				MaxSize:    cfg.Rotation.MaxSizeMB,
				MaxBackups: cfg.Rotation.RetainFiles,
				MaxAge:     cfg.Rotation.RetainDays,
				Compress:   cfg.Rotation.Compress,
			}
			writers[s] = lj
			l.closers = append(l.closers, lj)
		}
	}

	mk := func(s Stream) *slog.Logger {
		var h slog.Handler
		if cfg.Format == "text" {
			h = slog.NewTextHandler(writers[s], opts)
		} else {
			h = slog.NewJSONHandler(writers[s], opts)
		}
		return slog.New(h).With("stream", string(s))
	}
	l.App = mk(StreamApp)
	l.Audit = mk(StreamAudit)
	l.Security = mk(StreamSecurity)
	l.Job = mk(StreamJob)
	return l, nil
}

// Discard returns loggers that drop everything (tests, CLI subcommands).
func Discard() *Loggers {
	d := slog.New(slog.DiscardHandler)
	return &Loggers{App: d, Audit: d, Security: d, Job: d}
}

// RedactedValue replaces sensitive attribute values.
const RedactedValue = "«redacted»"

var sensitiveKeyParts = []string{
	"password", "passwd", "secret", "token", "authorization",
	"cookie", "private_key", "privatekey", "api_key", "apikey", "csrf",
}

// IsSensitiveKey reports whether an attribute or field name likely holds a
// credential.
func IsSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	if strings.HasSuffix(k, "_id") {
		return false // identifiers such as token_id or secret_id are safe to log
	}
	for _, p := range sensitiveKeyParts {
		if strings.Contains(k, p) {
			return true
		}
	}
	return false
}

// Redact is a slog ReplaceAttr function that masks values of sensitive keys.
// Value-based redaction of known secrets in job output is handled separately
// by the secrets package (M1).
func Redact(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() != slog.KindGroup && IsSensitiveKey(a.Key) {
		return slog.String(a.Key, RedactedValue)
	}
	return a
}

// syncWriter serializes writes from multiple handlers sharing one writer.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
