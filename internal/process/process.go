// Package process collects bounded, best-effort process counters without reading
// command lines, environments, smaps, or individual threads.
package process

import "context"

const MaxProcesses = 2048

type Counter struct {
	PID        int     `json:"pid"`
	StartTicks string  `json:"start_ticks"` // opaque identity; never convert to a JS number
	Name       string  `json:"name"`        // Linux comm (at most 15 bytes), not argv
	CPUSeconds float64 `json:"cpu_seconds"` // utime + stime, excludes children
	RSSBytes   uint64  `json:"rss_bytes"`
}

type Snapshot struct {
	At         int64     `json:"at"`
	Status     string    `json:"status"` // ok, partial, unavailable
	Skipped    int       `json:"skipped"`
	Truncated  bool      `json:"truncated"`
	DurationMS float64   `json:"duration_ms"`
	Items      []Counter `json:"items"`
}

func Read(ctx context.Context) *Snapshot { return read(ctx) }
