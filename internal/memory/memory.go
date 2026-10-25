package memory

import (
	"context"
	"errors"

	gopsutilmem "github.com/shirou/gopsutil/v4/mem"
)

type Snapshot struct {
	Total     uint64 `json:"total"`
	Available uint64 `json:"available"`
}

func Read(ctx context.Context) (Snapshot, error) {
	stats, err := gopsutilmem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return newSnapshot(stats.Total, stats.Available)
}

// newSnapshot preserves gopsutil's Available meaning rather than deriving it
// from Free memory.
func newSnapshot(total, available uint64) (Snapshot, error) {
	if total == 0 || available > total {
		return Snapshot{}, errors.New("invalid memory counters")
	}
	return Snapshot{Total: total, Available: available}, nil
}
