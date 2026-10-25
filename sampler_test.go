package hokaku

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	capturecpu "github.com/tetoraorg/hokaku/internal/cpu"
	capturememory "github.com/tetoraorg/hokaku/internal/memory"
)

func TestHostSampleWireContract(t *testing.T) {
	fixture, err := os.ReadFile("testdata/host.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writer, err := openHostSampleWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.close()
	for line := range strings.SplitSeq(strings.TrimSpace(string(fixture)), "\n") {
		var want hostSample
		if err := json.Unmarshal([]byte(line), &want); err != nil {
			t.Fatal(err)
		}
		var calls []string
		readers := hostReaders{
			cpu: func(context.Context) (capturecpu.Counters, error) { calls = append(calls, "cpu"); return want.CPU, nil },
			memory: func(context.Context) (capturememory.Snapshot, error) {
				calls = append(calls, "memory")
				return want.Memory, nil
			},
			now: func() time.Time { calls = append(calls, "now"); return time.UnixMilli(want.At) },
		}
		got, err := readHostSample(t.Context(), want.Environment, want.Host, readers)
		if err != nil {
			t.Fatal(err)
		}
		want.Scope = runtime.GOOS + "-host"
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
		if !slices.Equal(calls, []string{"cpu", "memory", "now"}) {
			t.Fatalf("read order: %v", calls)
		}
		if err := writer.write(got); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.syncAndClose(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "host.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.ReplaceAll(string(fixture), "linux-host", runtime.GOOS+"-host")
	if string(got) != want {
		t.Fatalf("wire format changed: %s", got)
	}
	if _, err := openHostSampleWriter(dir); err == nil {
		t.Fatal("must not overwrite samples")
	}
}

func TestHostSampleReaderErrors(t *testing.T) {
	sentinel := errors.New("read failed")
	for _, target := range []string{"cpu", "memory"} {
		t.Run(target, func(t *testing.T) {
			readers := hostReaders{
				cpu: func(context.Context) (capturecpu.Counters, error) {
					if target == "cpu" {
						return capturecpu.Counters{}, sentinel
					}
					return capturecpu.Counters{}, nil
				},
				memory: func(context.Context) (capturememory.Snapshot, error) {
					if target == "cpu" {
						t.Fatal("memory read after CPU failure")
					}
					return capturememory.Snapshot{}, sentinel
				},
				now: func() time.Time { t.Fatal("timestamp after failed read"); return time.Time{} },
			}
			_, err := readHostSample(t.Context(), "test", "app-1", readers)
			if !errors.Is(err, sentinel) || !strings.Contains(strings.ToLower(err.Error()), target) {
				t.Fatalf("missing error context: %v", err)
			}
		})
	}
}
