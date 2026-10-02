package control

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// EnvConfig is where the engine puts the node's hot+cold config as JSON.
const EnvConfig = "SYMPHONY_CONFIG"

// Config is the block's live configuration: defaults defined by the block,
// overridden by SYMPHONY_CONFIG at start, changed at runtime by config.set.
// Every Set is validated against the defined keys unless the key came from
// the environment (the topology may carry keys this block does not use).
type Config struct {
	mu       sync.RWMutex
	values   map[string]any
	defined  map[string]any // key -> default
	fromEnv  map[string]bool
	env      map[string]any // what SYMPHONY_CONFIG said, for Reset
	watchers []func(key string, value any)
}

func NewConfig() *Config {
	return &Config{values: map[string]any{}, defined: map[string]any{}, fromEnv: map[string]bool{}, env: map[string]any{}}
}

// Define declares a key with its default. Environment values already loaded
// win over the default.
func (c *Config) Define(key string, def any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.defined[key] = def
	if _, ok := c.values[key]; !ok {
		c.values[key] = def
	}
}

// Override replaces a key's current value unless the environment set it
// (blocks use it to change a common default, e.g. the gateway's retries).
func (c *Config) Override(key string, v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.defined[key] = v
	if !c.fromEnv[key] {
		c.values[key] = v
	}
}

// LoadEnv reads SYMPHONY_CONFIG.
func (c *Config) LoadEnv() error {
	raw := os.Getenv(EnvConfig)
	if raw == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return fmt.Errorf("config: %s: %w", EnvConfig, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range m {
		c.values[k] = v
		c.fromEnv[k] = true
		c.env[k] = v
	}
	return nil
}

// Reset puts every key back to what the block started with (the
// environment's value, else the default) and returns the keys it changed.
// Watchers are told, as for a Set.
func (c *Config) Reset() []string {
	c.mu.Lock()
	patch := map[string]any{}
	for k, cur := range c.values {
		start, ok := c.env[k]
		if !ok {
			start = c.defined[k]
		}
		if fmt.Sprint(cur) != fmt.Sprint(start) {
			patch[k] = start
			c.values[k] = start
		}
	}
	ws := append([]func(string, any){}, c.watchers...)
	c.mu.Unlock()
	keys := make([]string, 0, len(patch))
	for k, v := range patch {
		keys = append(keys, k)
		for _, w := range ws {
			w(k, v)
		}
	}
	sort.Strings(keys)
	return keys
}

// Watch calls fn with every key a Set or a Reset changes.
func (c *Config) Watch(fn func(key string, value any)) {
	c.mu.Lock()
	c.watchers = append(c.watchers, fn)
	c.mu.Unlock()
}

// Set applies a patch atomically after validating every key.
func (c *Config) Set(patch map[string]any) error {
	c.mu.Lock()
	for k, v := range patch {
		def, defined := c.defined[k]
		if !defined && !c.fromEnv[k] {
			c.mu.Unlock()
			return fmt.Errorf("config: unknown key %q", k)
		}
		if defined && !sameType(def, v) {
			c.mu.Unlock()
			return fmt.Errorf("config: key %q expects %T, got %T", k, def, v)
		}
	}
	// watchers hear of the keys that changed: setting a key to the value it
	// has (the engine pushes a node's whole config to bring it back to its
	// topology) must not, say, replace a worker pool that is in use
	changed := map[string]any{}
	for k, v := range patch {
		if cur, ok := c.values[k]; !ok || fmt.Sprint(cur) != fmt.Sprint(v) {
			changed[k] = v
		}
		c.values[k] = v
	}
	ws := append([]func(string, any){}, c.watchers...)
	c.mu.Unlock()
	for _, w := range ws {
		for k, v := range changed {
			w(k, v)
		}
	}
	return nil
}

func sameType(def, v any) bool {
	switch def.(type) {
	case float64, int, int64:
		switch v.(type) {
		case float64, int, int64:
			return true
		}
		return false
	case string:
		_, ok := v.(string)
		return ok
	case bool:
		_, ok := v.(bool)
		return ok
	}
	return true
}

// All returns a sorted copy of the effective config.
func (c *Config) All() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]any, len(c.values))
	for k, v := range c.values {
		out[k] = v
	}
	return out
}

func (c *Config) get(key string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.values[key]
	return v, ok
}

// Float returns a numeric key or def.
func (c *Config) Float(key string, def float64) float64 {
	v, ok := c.get(key)
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return def
}

// Int returns a numeric key as int or def.
func (c *Config) Int(key string, def int) int {
	return int(c.Float(key, float64(def)))
}

// Duration reads a `_ms` key.
func (c *Config) Duration(key string, def time.Duration) time.Duration {
	if _, ok := c.get(key); !ok {
		return def
	}
	return time.Duration(c.Float(key, 0) * float64(time.Millisecond))
}

// String returns a string key or def.
func (c *Config) String(key, def string) string {
	v, ok := c.get(key)
	if !ok {
		return def
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// Bool returns a bool key or def.
func (c *Config) Bool(key string, def bool) bool {
	v, ok := c.get(key)
	if !ok {
		return def
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}
