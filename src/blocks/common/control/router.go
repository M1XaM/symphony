// Package control is the /control endpoint: one JSON command in, one result
// out. It owns the config store, the fault store and the event stream every
// block shares, and lets each block register the operations only it knows.
package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"symphony/blocks/common/faults"
)

// Command is the request body of POST /control.
type Command struct {
	ID         string         `json:"id,omitempty"`
	Op         string         `json:"op"`
	Target     faults.Target  `json:"target,omitempty"`
	Params     map[string]any `json:"params,omitempty"`
	DurationMs int            `json:"duration_ms,omitempty"`
}

// Duration converts DurationMs.
func (c Command) Duration() time.Duration { return time.Duration(c.DurationMs) * time.Millisecond }

// Float reads a numeric param.
func (c Command) Float(key string, def float64) float64 {
	switch v := c.Params[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return def
}

// Int reads a numeric param as int.
func (c Command) Int(key string, def int) int { return int(c.Float(key, float64(def))) }

// String reads a string param.
func (c Command) String(key, def string) string {
	if s, ok := c.Params[key].(string); ok {
		return s
	}
	return def
}

// Result is the response body.
type Result struct {
	ID    string `json:"id,omitempty"`
	Op    string `json:"op"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	State any    `json:"state,omitempty"`
}

// Handler executes one operation and returns the state to report.
type Handler func(cmd Command) (any, error)

// ErrBadRequest marks a client mistake (400 rather than 500).
var ErrBadRequest = errors.New("bad request")

// Router dispatches commands.
type Router struct {
	Config *Config
	Faults *faults.Store
	Events *Events

	mu       sync.RWMutex
	handlers map[string]Handler
	// Reset is what state.reset calls after clearing faults; blocks set it.
	Reset func()
}

func NewRouter(cfg *Config, ev *Events) *Router {
	r := &Router{Config: cfg, Events: ev, handlers: map[string]Handler{}}
	r.Faults = faults.NewStore(func(event string, f faults.Fault) { ev.Publish(event, f) })
	r.registerCommon()
	return r
}

// Handle registers an operation.
func (r *Router) Handle(op string, h Handler) {
	r.mu.Lock()
	r.handlers[op] = h
	r.mu.Unlock()
}

// Ops lists the registered operations.
func (r *Router) Ops() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ops := make([]string, 0, len(r.handlers))
	for op := range r.handlers {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	return ops
}

// Execute runs a command.
func (r *Router) Execute(cmd Command) Result {
	res, _ := r.execute(cmd)
	return res
}

// execute runs a command and also returns the handler's error as it was,
// so the HTTP layer can tell a client mistake from a failure.
func (r *Router) execute(cmd Command) (Result, error) {
	r.mu.RLock()
	h, ok := r.handlers[cmd.Op]
	r.mu.RUnlock()
	res := Result{ID: cmd.ID, Op: cmd.Op}
	if !ok {
		err := fmt.Errorf("%w: unknown op %q", ErrBadRequest, cmd.Op)
		res.Error = err.Error()
		return res, err
	}
	state, err := h(cmd)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	res.OK = true
	res.State = state
	return res, nil
}

// HTTPHandler serves POST /control.
func (r *Router) HTTPHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "POST a command", http.StatusMethodNotAllowed)
			return
		}
		var cmd Command
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<20)).Decode(&cmd); err != nil {
			writeJSON(w, http.StatusBadRequest, Result{OK: false, Error: "decode: " + err.Error()})
			return
		}
		res, err := r.execute(cmd)
		status := http.StatusOK
		if err != nil {
			status = http.StatusInternalServerError
			if errors.Is(err, ErrBadRequest) || isBad(err.Error()) {
				status = http.StatusBadRequest
			}
		}
		writeJSON(w, status, res)
	}
}

// isBad recognises the client mistakes that handlers report without
// wrapping ErrBadRequest (the config and fault stores have their own
// errors).
func isBad(msg string) bool {
	for _, p := range []string{"unknown key", "unknown kind", "expects", "must be", "required"} {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// registerCommon wires the operations every block has.
func (r *Router) registerCommon() {
	r.Handle(OpConfigGet, func(cmd Command) (any, error) {
		return r.Config.All(), nil
	})
	r.Handle(OpConfigSet, func(cmd Command) (any, error) {
		if len(cmd.Params) == 0 {
			return nil, fmt.Errorf("%w: params required", ErrBadRequest)
		}
		if err := r.Config.Set(cmd.Params); err != nil {
			return nil, err
		}
		r.Events.Publish("config.set", cmd.Params)
		return r.Config.All(), nil
	})
	// config.reset undoes every config.set since the block started
	r.Handle(OpConfigReset, func(cmd Command) (any, error) {
		keys := r.Config.Reset()
		r.Events.Publish("config.reset", map[string]any{"keys": keys})
		return r.Config.All(), nil
	})
	r.Handle(OpFaultSet, func(cmd Command) (any, error) {
		kind := faults.Kind(cmd.String("kind", ""))
		if kind == "" {
			return nil, fmt.Errorf("%w: params.kind required", ErrBadRequest)
		}
		params := map[string]any{}
		for k, v := range cmd.Params {
			if k != "kind" {
				params[k] = v
			}
		}
		f, err := r.Faults.Set(kind, cmd.Target, params, cmd.Duration())
		if err != nil {
			return nil, err
		}
		return f, nil
	})
	r.Handle(OpFaultClear, func(cmd Command) (any, error) {
		if all, _ := cmd.Params["all"].(bool); all {
			return map[string]any{"cleared": r.Faults.ClearAll()}, nil
		}
		id := cmd.String("id", "")
		if id == "" {
			return nil, fmt.Errorf("%w: params.id or params.all required", ErrBadRequest)
		}
		if !r.Faults.Clear(id) {
			return nil, fmt.Errorf("%w: no fault %q", ErrBadRequest, id)
		}
		return map[string]any{"cleared": 1}, nil
	})
	r.Handle(OpFaultList, func(cmd Command) (any, error) {
		list := r.Faults.List()
		out := make([]map[string]any, 0, len(list))
		for _, f := range list {
			out = append(out, map[string]any{"id": f.ID, "kind": f.Kind, "target": f.Target, "params": f.Params, "remaining_ms": f.RemainingMs()})
		}
		return out, nil
	})
	r.Handle(OpStateReset, func(cmd Command) (any, error) {
		n := r.Faults.ClearAll()
		if r.Reset != nil {
			r.Reset()
		}
		r.Events.Publish("state.reset", nil)
		return map[string]any{"faults_cleared": n}, nil
	})
	r.Handle(OpOps, func(cmd Command) (any, error) { return r.Ops(), nil })
}
