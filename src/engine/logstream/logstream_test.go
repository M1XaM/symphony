package logstream

import (
	"strings"
	"testing"
	"time"
)

func TestParseBlockRecord(t *testing.T) {
	line := `{"time":"2026-09-18T10:00:00Z","level":"WARN","msg":"saga stalled","node":"orders","kind":"service","lag":7,"topic":"orders.events"}`
	level, msg := parse(line, false)
	if level != "warn" {
		t.Fatalf("level = %q, want warn", level)
	}
	// the attrs the container name already implies are dropped; the rest are
	// appended in a stable order
	if msg != "saga stalled lag=7 topic=orders.events" {
		t.Fatalf("msg = %q", msg)
	}
}

func TestParseKeepsWhatItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name, line, level string
		stderr            bool
	}{
		{"plain stdout", "starting up", "info", false},
		{"panic on stderr", "panic: nil map", "error", true},
		{"json without a message", `{"level":"INFO"}`, "info", false},
		{"broken json", `{"level":"INFO"`, "info", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			level, msg := parse(tc.line, tc.stderr)
			if level != tc.level {
				t.Fatalf("level = %q, want %q", level, tc.level)
			}
			if msg != tc.line {
				t.Fatalf("msg = %q, want the line unchanged", msg)
			}
		})
	}
}

func TestLimiterThrottlesAndSaysSo(t *testing.T) {
	l := &limiter{tokens: 2, last: time.Now()}
	for i := 0; i < 2; i++ {
		if drop, _ := l.take(); drop {
			t.Fatalf("line %d dropped while tokens were left", i)
		}
	}
	drop, notice := l.take()
	if !drop {
		t.Fatal("the bucket is empty; the line should have been dropped")
	}
	if notice != 1 {
		t.Fatalf("notice = %d, want the first drop announced", notice)
	}
	// the announcement is rate-limited too, so a flood is one warning, not two
	if drop, notice := l.take(); !drop || notice != 0 {
		t.Fatalf("drop=%v notice=%d, want a silent drop", drop, notice)
	}
}

func TestParseLeavesLongLinesAlone(t *testing.T) {
	long := strings.Repeat("x", 5000)
	if _, msg := parse(long, false); msg != long {
		t.Fatal("a long line must survive intact")
	}
}
