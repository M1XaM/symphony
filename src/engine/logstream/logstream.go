// Package logstream mirrors container output into the event bus. The blocks
// write JSON lines to stdout, which Docker keeps and `docker logs -f` prints;
// the Streamer follows every container the engine owns and republishes each
// line as an engine log, so the app's Log tab holds the container logs too and
// nobody has to keep a terminal open next to the window.
package logstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"symphony/engine/events"
	"symphony/engine/runtime"
)

const (
	// how often to look for containers that appeared since the last sweep
	scanInterval = 2 * time.Second
	// A block logs a handful of lines per lifetime, so anything beyond this
	// is a runaway loop; it gets throttled rather than drowning the tab.
	linesPerSecond = 40
	burst          = 200
	// how often a throttled container may say that it is being throttled
	dropNoticeEvery = 5 * time.Second
)

// attrs the blocks put on every record; the container name in the log line's
// source already says as much, so they are not repeated in the message.
var skipAttrs = map[string]bool{"time": true, "level": true, "msg": true, "node": true, "kind": true, "replica": true, "deployment": true}

// Streamer follows container logs for as long as it is running. Safe for
// concurrent use; Start and Stop may be called once each.
type Streamer struct {
	bus *events.Bus

	rt        runtime.Runtime
	mu        sync.Mutex
	enabled   bool
	following map[string]*tail // container id -> the goroutine tailing it
	// when the mirror (re)started, and when each container's last stream
	// ended: a stream starts from the latest of those and the container's
	// creation, so nothing is replayed and nothing printed in between (a
	// new container's first lines, a restarted one's) is lost
	since  time.Time
	ended  map[string]time.Time
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// tail is one container's follower, kept so it can be cancelled by id.
type tail struct{ cancel context.CancelFunc }

func New(rt runtime.Runtime, bus *events.Bus) *Streamer {
	return &Streamer{bus: bus, rt: rt, enabled: true, following: map[string]*tail{}, ended: map[string]time.Time{}}
}

// Enabled reports whether lines are currently being mirrored.
func (s *Streamer) Enabled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

// SetEnabled turns the mirror on or off. Turning it off stops every follower;
// turning it back on picks the containers up again from that moment, so the
// tab is never backfilled with what happened while it was muted.
func (s *Streamer) SetEnabled(on bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	changed := s.enabled != on
	s.enabled = on
	if changed && on {
		s.since = time.Now()
	}
	var stop []context.CancelFunc
	if !on {
		for id, t := range s.following {
			stop = append(stop, t.cancel)
			delete(s.following, id)
		}
	}
	s.mu.Unlock()
	for _, cancel := range stop {
		cancel()
	}
	if changed {
		state := "off"
		if on {
			state = "on"
		}
		s.bus.Log("info", "logs", "container log streaming "+state)
	}
}

// Start begins sweeping for containers. It returns immediately.
func (s *Streamer) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.since = time.Now()
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.loop(ctx)
	}()
}

// Stop ends every follower and waits for them.
func (s *Streamer) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

func (s *Streamer) loop(ctx context.Context) {
	t := time.NewTicker(scanInterval)
	defer t.Stop()
	for {
		s.scan(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// scan starts a follower for every container that does not have one, and
// forgets the ones the runtime no longer reports. Running containers are all
// it can tail, so it asks for exactly those: one call to the daemon per
// sweep, whatever else is on the machine.
func (s *Streamer) scan(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	cs, err := s.rt.ListHost(ctx, false)
	if err != nil {
		return
	}
	live := make(map[string]bool, len(cs))
	for _, c := range cs {
		if !c.Ours {
			continue
		}
		live[c.ID] = true
		s.mu.Lock()
		_, following := s.following[c.ID]
		if following || !s.enabled {
			s.mu.Unlock()
			continue
		}
		fctx, cancel := context.WithCancel(ctx)
		t := &tail{cancel: cancel}
		s.following[c.ID] = t
		since := s.since
		if created := time.UnixMilli(c.CreatedAt); created.After(since) {
			since = created
		}
		if ended := s.ended[c.ID]; ended.After(since) {
			since = ended
		}
		s.mu.Unlock()
		s.wg.Add(1)
		go func(c runtime.HostContainer) {
			defer s.wg.Done()
			defer cancel()
			s.follow(fctx, c, since)
			s.mu.Lock()
			s.ended[c.ID] = time.Now()
			if s.following[c.ID] == t { // a restart may already own the slot
				delete(s.following, c.ID)
			}
			s.mu.Unlock()
		}(c)
	}
	// drop followers of containers that are gone, so a re-created one (which
	// gets a new id) is picked up again
	s.mu.Lock()
	for id, t := range s.following {
		if !live[id] {
			t.cancel()
			delete(s.following, id)
		}
	}
	// a stopped container may be started again, so its end is kept a while
	for id, at := range s.ended {
		if !live[id] && time.Since(at) > time.Hour {
			delete(s.ended, id)
		}
	}
	s.mu.Unlock()
}

// follow tails one container until it stops, from since on: what it wrote
// before belongs to a previous session of the app, or was mirrored already.
func (s *Streamer) follow(ctx context.Context, c runtime.HostContainer, since time.Time) {
	lim := &limiter{tokens: burst, last: time.Now()}
	err := s.rt.FollowLogs(ctx, c.Name, since, func(line string, stderr bool) {
		s.emit(c.Name, line, stderr, lim)
	})
	// a container removed while followed is the normal end of its stream
	if err != nil && ctx.Err() == nil && !errors.Is(err, runtime.ErrNotFound) && !strings.Contains(err.Error(), "marked for removal") {
		s.bus.Log("debug", "logs", fmt.Sprintf("%s: %v", c.Name, err))
	}
}

func (s *Streamer) emit(container, line string, stderr bool, lim *limiter) {
	if !s.Enabled() {
		return
	}
	switch dropped, notice := lim.take(); {
	case notice > 0:
		s.bus.Log("warn", container, fmt.Sprintf("… %d line(s) dropped: logging faster than %d lines/s", notice, linesPerSecond))
		return
	case dropped:
		return
	}
	level, msg := parse(line, stderr)
	s.bus.Log(level, container, msg)
}

// parse turns a block's JSON record into a flat line, and leaves anything
// else (a panic, a runtime message) as it was.
func parse(line string, stderr bool) (level, msg string) {
	level = "info"
	if stderr {
		level = "error"
	}
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{") {
		return level, line
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(trimmed), &rec); err != nil {
		return level, line
	}
	if lv, ok := rec["level"].(string); ok && lv != "" {
		level = strings.ToLower(lv)
	}
	msg, _ = rec["msg"].(string)
	if msg == "" {
		return level, line
	}
	keys := make([]string, 0, len(rec))
	for k := range rec {
		if !skipAttrs[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(msg)
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%v", k, rec[k])
	}
	return level, b.String()
}

// limiter is a token bucket, one per container.
type limiter struct {
	tokens  float64
	last    time.Time
	dropped int
	noticed time.Time
}

// take reports whether the line must be dropped, and how many drops to
// announce (0 = say nothing).
func (l *limiter) take() (drop bool, notice int) {
	now := time.Now()
	l.tokens += now.Sub(l.last).Seconds() * linesPerSecond
	if l.tokens > burst {
		l.tokens = burst
	}
	l.last = now
	if l.tokens >= 1 {
		l.tokens--
		return false, 0
	}
	l.dropped++
	if now.Sub(l.noticed) >= dropNoticeEvery {
		l.noticed = now
		n := l.dropped
		l.dropped = 0
		return true, n
	}
	return true, 0
}
