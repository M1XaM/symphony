package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

const logCap = 1000

// LogLine is one buffered log record.
type LogLine struct {
	Ts    int64          `json:"ts"`
	Level string         `json:"level"`
	Msg   string         `json:"msg"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// LogBuffer is a slog handler that writes JSON lines to stdout (Docker's
// log driver) and keeps the last logCap lines for /logs. Handlers derived
// with WithAttrs/WithGroup share the same ring.
type LogBuffer struct {
	ring  *logRing
	level *slog.LevelVar
	out   slog.Handler
	attrs []slog.Attr
}

type logRing struct {
	mu    sync.Mutex
	lines []LogLine
}

func NewLogBuffer() *LogBuffer {
	lv := &slog.LevelVar{}
	lv.Set(slog.LevelInfo)
	return &LogBuffer{ring: &logRing{}, level: lv, out: slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lv})}
}

// SetLevel changes the level at runtime (log.level).
func (l *LogBuffer) SetLevel(s string) error {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(s)); err != nil {
		return err
	}
	l.level.Set(lv)
	return nil
}

func (l *LogBuffer) Level() string { return l.level.Level().String() }

// Enabled is true for every level: the ring keeps everything so /logs is
// useful at any log_level; the level only filters what reaches stdout.
func (l *LogBuffer) Enabled(ctx context.Context, lv slog.Level) bool { return lv >= slog.LevelDebug }

func (l *LogBuffer) Handle(ctx context.Context, r slog.Record) error {
	line := LogLine{Ts: r.Time.UnixMilli(), Level: r.Level.String(), Msg: r.Message}
	if len(l.attrs) > 0 || r.NumAttrs() > 0 {
		line.Attrs = map[string]any{}
		for _, a := range l.attrs {
			line.Attrs[a.Key] = a.Value.Any()
		}
		r.Attrs(func(a slog.Attr) bool {
			line.Attrs[a.Key] = a.Value.Any()
			return true
		})
	}
	l.ring.mu.Lock()
	if len(l.ring.lines) >= logCap {
		l.ring.lines = l.ring.lines[1:]
	}
	l.ring.lines = append(l.ring.lines, line)
	l.ring.mu.Unlock()
	if !l.out.Enabled(ctx, r.Level) {
		return nil
	}
	return l.out.Handle(ctx, r)
}

func (l *LogBuffer) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &LogBuffer{ring: l.ring, level: l.level, out: l.out.WithAttrs(attrs), attrs: append(append([]slog.Attr{}, l.attrs...), attrs...)}
}

func (l *LogBuffer) WithGroup(name string) slog.Handler {
	return &LogBuffer{ring: l.ring, level: l.level, out: l.out.WithGroup(name), attrs: l.attrs}
}

// Recent returns the last n lines.
func (l *LogBuffer) Recent(n int) []LogLine {
	l.ring.mu.Lock()
	defer l.ring.mu.Unlock()
	if n <= 0 || n > len(l.ring.lines) {
		n = len(l.ring.lines)
	}
	return append([]LogLine{}, l.ring.lines[len(l.ring.lines)-n:]...)
}

// Handler serves GET /logs?n=200.
func (l *LogBuffer) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n == 0 {
			n = 200
		}
		lines := l.Recent(n)
		if lines == nil {
			lines = []LogLine{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ts": time.Now().UnixMilli(), "lines": lines})
	}
}
