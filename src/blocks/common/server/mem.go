package server

import "runtime"

// readMem reports the Go runtime's view of memory (the cgroup limit is the
// engine's business; it knows what it set).
func readMem() map[string]any {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return map[string]any{
		"heap_mb":    ms.HeapAlloc >> 20,
		"sys_mb":     ms.Sys >> 20,
		"gc_count":   ms.NumGC,
		"goroutines": runtime.NumGoroutine(),
	}
}
