package capture

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	capturecpu "github.com/tetoraorg/hokaku/internal/cpu"
	capturememory "github.com/tetoraorg/hokaku/internal/memory"
)

type hostSample struct {
	Version     int                    `json:"version"`
	At          int64                  `json:"at"`
	Environment string                 `json:"environment"`
	Host        string                 `json:"host"`
	Scope       string                 `json:"scope"`
	CPU         capturecpu.Counters    `json:"cpu"`
	Memory      capturememory.Snapshot `json:"memory"`
}

type hostReaders struct {
	cpu    func(context.Context) (capturecpu.Counters, error)
	memory func(context.Context) (capturememory.Snapshot, error)
	now    func() time.Time
}

func defaultHostReaders() hostReaders {
	return hostReaders{cpu: capturecpu.Read, memory: capturememory.Read, now: time.Now}
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
	return hostSample{
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
