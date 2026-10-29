//go:build linux

package process

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stat(pid int, name string) string {
	fields := strings.Fields("S 0 0 0 0 0 0 0 0 0 0 125 75 999 999 0 0 1 0 12345 8192 2")
	return fmt.Sprintf("%d (%s) %s\n", pid, name, strings.Join(fields, " "))
}
func TestParseStat(t *testing.T) {
	p, err := parseStat(stat(12, "nginx ) worker"), 100, 4096)
	if err != nil || p.PID != 12 || p.Name != "nginx ) worker" || p.CPUSeconds != 2 || p.StartTicks != "12345" || p.RSSBytes != 8192 {
		t.Fatalf("%+v %v", p, err)
	}
	for _, bad := range []string{"", "12 (nginx) S", strings.Replace(stat(12, "x"), "125", "oops", 1), strings.Replace(stat(12, "x"), "8192 2", "8192 -1", 1)} {
		if _, err := parseStat(bad, 100, 4096); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
func fakeProc(t testing.TB, n int) string {
	t.Helper()
	root := t.TempDir()
	for i := 1; i <= n; i++ {
		dir := filepath.Join(root, fmt.Sprint(i))
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat(i, "nginx")), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func TestScanBoundsAndFailures(t *testing.T) {
	root := fakeProc(t, 3)
	s := scan(t.Context(), root, 100, nil, 2)
	if s.Status != "partial" || !s.Truncated || len(s.Items) != 2 {
		t.Fatalf("%+v", s)
	}
	if err := os.Remove(filepath.Join(root, "2", "stat")); err != nil {
		t.Fatal(err)
	}
	s = scan(t.Context(), root, 100, nil, 10)
	if s.Status != "partial" || s.Skipped != 1 || s.Truncated || len(s.Items) != 2 {
		t.Fatalf("%+v", s)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if s := scan(ctx, root, 100, nil, 10); !s.Truncated || len(s.Items) != 0 {
		t.Fatalf("%+v", s)
	}
	if s := scan(t.Context(), root, 100, nil, 10); s.Truncated {
		t.Fatalf("%+v", s)
	}
	if s := scan(t.Context(), root+"/missing", 100, nil, 10); s.Status != "unavailable" {
		t.Fatal(s)
	}
}
func TestReadSelf(t *testing.T) {
	s := Read(t.Context())
	for _, p := range s.Items {
		if p.PID == os.Getpid() {
			if p.RSSBytes == 0 || p.StartTicks == "" {
				t.Fatal(p)
			}
			return
		}
	}
	t.Fatalf("own process not found: %+v", s)
}
func BenchmarkScanAndEncode(b *testing.B) {
	for _, n := range []int{100, 1000, 2048} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			root := fakeProc(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				s := scan(context.Background(), root, 100, nil, MaxProcesses)
				data, err := json.Marshal(s)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(len(data)), "bytes/sample")
			}
		})
	}

}

func TestScanSurvivesSchedulerDelay(t *testing.T) {
	root := fakeProc(t, 3)
	reads := 0
	s := scanWithReader(t.Context(), root, 100, nil, 10, func(path string) ([]byte, error) {
		reads++
		if reads == 1 {
			time.Sleep(35 * time.Millisecond)
		}
		return os.ReadFile(path)
	})
	if s.Status != "ok" || s.Truncated || len(s.Items) != 3 || s.DurationMS < 25 {
		t.Fatalf("scheduler delay discarded valid processes: %+v", s)
	}
}

func TestScanCancelsBetweenReads(t *testing.T) {
	root := fakeProc(t, 3)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := scanWithReader(ctx, root, 100, nil, 10, func(path string) ([]byte, error) {
		cancel()
		return os.ReadFile(path)
	})
	if s.Status != "partial" || !s.Truncated || len(s.Items) != 1 {
		t.Fatalf("cancellation must preserve completed reads: %+v", s)
	}
}
