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

func TestEntryKeepsTheBlocksTime(t *testing.T) {
	e := Entry("demo-payment", `{"time":"2026-10-03T10:00:00.5Z","level":"WARN","msg":"slow","node":"payment","ms":12}`)
	if e.Level != "warn" || e.Source != "demo-payment" || e.Message != "slow ms=12" {
		t.Fatalf("entry = %+v", e)
	}
	if want := time.Date(2026, 10, 3, 10, 0, 0, 500e6, time.UTC).UnixMilli(); e.Ts != want {
		t.Fatalf("ts = %d, want %d", e.Ts, want)
	}
	if e := Entry("demo-payment", "panic: boom"); e.Ts != 0 || e.Message != "panic: boom" {
		t.Fatalf("plain line = %+v", e)
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
