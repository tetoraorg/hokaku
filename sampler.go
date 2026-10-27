package hokaku

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	gopsutilnet "github.com/shirou/gopsutil/v4/net"

	capturecpu "github.com/tetoraorg/hokaku/internal/cpu"
	capturememory "github.com/tetoraorg/hokaku/internal/memory"
)

type networkCounters struct {
	ReceivedBytes uint64 `json:"received_bytes"`
	SentBytes     uint64 `json:"sent_bytes"`
}

func readNetwork(ctx context.Context) (networkCounters, error) {
	stats, err := gopsutilnet.IOCountersWithContext(ctx, false)
	if err != nil {
		return networkCounters{}, err
	}
	if len(stats) != 1 {
		return networkCounters{}, fmt.Errorf("expected aggregate network counters")
	}
	return networkCounters{ReceivedBytes: stats[0].BytesRecv, SentBytes: stats[0].BytesSent}, nil
}

type hostSample struct {
	Version     int                    `json:"version"`
	At          int64                  `json:"at"`
	Environment string                 `json:"environment"`
	Host        string                 `json:"host"`
	Scope       string                 `json:"scope"`
	CPU         capturecpu.Counters    `json:"cpu"`
	Memory      capturememory.Snapshot `json:"memory"`
	Network     *networkCounters       `json:"network,omitempty"`
}

type hostReaders struct {
	network func(context.Context) (networkCounters, error)
	cpu     func(context.Context) (capturecpu.Counters, error)
	memory  func(context.Context) (capturememory.Snapshot, error)
	now     func() time.Time
}

func defaultHostReaders() hostReaders {
	return hostReaders{cpu: capturecpu.Read, memory: capturememory.Read, network: readNetwork, now: time.Now}
}

func readHostSample(ctx context.Context, environment, hostname string, readers hostReaders) (hostSample, error) {
	cpuValues, err := readers.cpu(ctx)
	if err != nil {
		return hostSample{}, fmt.Errorf("CPU counters: %w", err)
	}
	memoryValues, err := readers.memory(ctx)
	if err != nil {
		return hostSample{}, fmt.Errorf("memory snapshot: %w", err)
	}
	var network *networkCounters
	if readers.network != nil {
		if value, err := readers.network(ctx); err == nil {
			network = &value
		}
	}
	return hostSample{
		Network:     network,
		Version:     1,
		At:          readers.now().UnixMilli(),
		Environment: environment,
		Host:        hostname,
		Scope:       runtime.GOOS + "-host",
		CPU:         cpuValues,
		Memory:      memoryValues,
	}, nil
}

const hostJSONLName = "host.jsonl"

// hostSampleWriter owns the host.jsonl artifact's file and encoder lifecycle.
type hostSampleWriter struct {
	file    *os.File
	encoder *json.Encoder
	closed  bool
}

func openHostSampleWriter(dir string) (*hostSampleWriter, error) {
	file, err := os.OpenFile(filepath.Join(dir, hostJSONLName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	return &hostSampleWriter{file: file, encoder: json.NewEncoder(file)}, nil
}

func (w *hostSampleWriter) write(sample hostSample) error {
	return w.encoder.Encode(sample)
}

func (w *hostSampleWriter) syncAndClose() error {
	if err := w.file.Sync(); err != nil {
		return err
	}
	err := w.file.Close()
	w.closed = true
	return err
}

func (w *hostSampleWriter) close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	return w.file.Close()
}
