//go:build linux

package process

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tklauser/go-sysconf"
)

var clockTicks, clockErr = sysconf.Sysconf(sysconf.SC_CLK_TCK)

func read(ctx context.Context) *Snapshot {
	return scan(ctx, "/proc", clockTicks, clockErr, MaxProcesses)
}

// Bound work by process count and cancellation, not wall-clock time: scheduler
// contention during a benchmark must not discard the tail of every /proc scan.
// Like the OTel hostmetrics process scraper, keep successful per-process reads.
func scan(ctx context.Context, root string, ticks int64, tickErr error, limit int) *Snapshot {
	return scanWithReader(ctx, root, ticks, tickErr, limit, os.ReadFile)
}

func scanWithReader(ctx context.Context, root string, ticks int64, tickErr error, limit int, readFile func(string) ([]byte, error)) *Snapshot {
	started := time.Now()
	s := &Snapshot{At: started.UnixMilli(), Status: "ok", Items: make([]Counter, 0)}
	defer func() { s.DurationMS = float64(time.Since(started)) / float64(time.Millisecond) }()
	if tickErr != nil || ticks <= 0 {
		s.Status = "unavailable"
		return s
	}
	dir, err := os.Open(root)
	if err != nil {
		s.Status = "unavailable"
		return s
	}
	defer dir.Close()
	attempts := 0
	for {
		names, err := dir.Readdirnames(128)
		if err != nil && err != io.EOF {
			s.Status = "partial"
			s.Truncated = true
			return s
		}
		for _, name := range names {
			pid, err := strconv.Atoi(name)
			if err != nil || pid <= 0 {
				continue
			}
			if ctx.Err() != nil || attempts >= limit {
				s.Status = "partial"
				s.Truncated = true
				return s
			}
			attempts++
			data, err := readFile(filepath.Join(root, name, "stat"))
			if err != nil {
				s.Skipped++
				s.Status = "partial"
				continue
			}
			p, err := parseStat(string(data), ticks, uint64(os.Getpagesize()))
			if err != nil || p.PID != pid {
				s.Skipped++
				s.Status = "partial"
				continue
			}
			s.Items = append(s.Items, p)
		}
		if err == io.EOF {
			return s
		}
	}
}

func parseStat(stat string, ticks int64, pageSize uint64) (Counter, error) {
	var p Counter
	// comm may contain spaces and ')'; the final ')' terminates field 2.
	open, close := strings.IndexByte(stat, '('), strings.LastIndexByte(stat, ')')
	if open < 1 || close <= open || ticks <= 0 {
		return p, fmt.Errorf("invalid stat")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(stat[:open]))
	if err != nil || pid <= 0 {
		return p, fmt.Errorf("invalid pid")
	}
	fields := strings.Fields(stat[close+1:]) // starts at field 3 (state)
	if len(fields) < 22 {
		return p, fmt.Errorf("short stat")
	}
	user, e1 := strconv.ParseUint(fields[11], 10, 64) // field 14
	system, e2 := strconv.ParseUint(fields[12], 10, 64)
	start, e3 := strconv.ParseUint(fields[19], 10, 64) // field 22
	rss, e4 := strconv.ParseInt(fields[21], 10, 64)    // field 24
	name := strings.ToValidUTF8(stat[open+1:close], "�")
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || rss < 0 || name == "" || len(name) > 256 || uint64(rss) > 9007199254740991/pageSize {
		return p, fmt.Errorf("invalid counters")
	}
	return Counter{PID: pid, StartTicks: strconv.FormatUint(start, 10), Name: name,
		CPUSeconds: float64(user)/float64(ticks) + float64(system)/float64(ticks), RSSBytes: uint64(rss) * pageSize}, nil
}
