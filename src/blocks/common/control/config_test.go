package control

import (
	"reflect"
	"testing"
)

// Reset returns every key to what the block started with: the value the
// environment gave, else the default.
func TestConfigReset(t *testing.T) {
	t.Setenv(EnvConfig, `{"cpu_ms": 4, "workers": 16}`)
	c := NewConfig()
	c.Define("cpu_ms", 2.0)
	c.Define("io_ms", 0.0)
	c.Define("log_level", "info")
	if err := c.LoadEnv(); err != nil {
		t.Fatal(err)
	}
	var told []string
	c.Watch(func(key string, _ any) { told = append(told, key) })
	if err := c.Set(map[string]any{"cpu_ms": 15.0, "io_ms": 30.0, "workers": 1.0}); err != nil {
		t.Fatal(err)
	}
	told = nil

	changed := c.Reset()
	if want := []string{"cpu_ms", "io_ms", "workers"}; !reflect.DeepEqual(changed, want) {
		t.Fatalf("Reset changed %v, want %v", changed, want)
	}
	if len(told) != 3 {
		t.Fatalf("watchers were told of %v", told)
	}
	if c.Float("cpu_ms", 0) != 4 || c.Float("io_ms", -1) != 0 || c.Int("workers", 0) != 16 || c.String("log_level", "") != "info" {
		t.Fatalf("after Reset: %v", c.All())
	}
	if again := c.Reset(); len(again) != 0 {
		t.Fatalf("a second Reset changed %v", again)
	}
}

// Watchers hear of changes, not of a key being set to the value it has.
func TestSetNotifiesChangesOnly(t *testing.T) {
	c := NewConfig()
	c.Define("workers", 32.0)
	c.Define("cpu_ms", 2.0)
	var told []string
	c.Watch(func(key string, _ any) { told = append(told, key) })
	if err := c.Set(map[string]any{"workers": 32.0, "cpu_ms": 5.0}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(told, []string{"cpu_ms"}) {
		t.Fatalf("watchers were told of %v", told)
	}
}
