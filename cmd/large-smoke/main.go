// Large-smoke creates a synthetic completed capture, uploads it and retries it.
// It is opt-in and intended only for an isolated kosakana test server.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/pprof/profile"
	"github.com/tetoraorg/hokaku"
)

func main() {
	endpoint := flag.String("endpoint", "http://127.0.0.1:3000", "isolated test server")
	out := flag.String("out", "", "new private output directory (required)")
	size := flag.Int64("bytes", 1<<30, "exact slow.log byte count")
	captureID := flag.String("capture-id", "large-smoke", "new capture ID")
	host := flag.String("host", "app-1", "host name")
	retry := flag.Bool("retry", true, "repeat Upload to check idempotency")
	flag.Parse()
	if *out == "" || *size < 16384 {
		log.Fatal("--out required and --bytes must be at least 16384")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, *endpoint, *out, *captureID, *host, *size, *retry); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, endpoint, out, id, host string, size int64, retry bool) error {
	if err := os.Mkdir(out, 0700); err != nil {
		return err
	}
	recorder, err := hokaku.New(hokaku.Config{Endpoint: endpoint, Token: os.Getenv("KOSAKANA_ACCESS_KEY"), Environment: "large-check", Host: host, OutputDir: out, Rotate: func(context.Context) error { return nil }})
	if err != nil {
		return err
	}
	defer recorder.Close()
	// Use a basename only; the normal Upload validation also checks identity.
	if id == "." || id == ".." || filepath.Base(id) != id {
		return fmt.Errorf("invalid capture ID")
	}
	dir := filepath.Join(out, id)
	if err = os.Mkdir(dir, 0700); err != nil {
		return err
	}
	now := time.Now().Truncate(time.Second)
	at := now.UnixMilli()
	record := fmt.Sprintf("# Time: %s\n# User@Host: root[root] @ localhost []\n# Query_time: 0.100000 Lock_time: 0.000000 Rows_sent: 1 Rows_examined: 1\nSET timestamp=%d;\nSELECT 42 /*%s*/;\n", now.UTC().Format(time.RFC3339), now.Unix(), strings.Repeat("x", 8192))
	records := size / int64(len(record))
	slow, err := os.OpenFile(filepath.Join(dir, "slow.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	buffered := bufio.NewWriterSize(slow, 256<<10)
	for range records {
		if err = ctx.Err(); err != nil {
			slow.Close()
			return err
		}
		if _, err = buffered.WriteString(record); err != nil {
			slow.Close()
			return err
		}
	}
	if _, err = buffered.WriteString(strings.Repeat("\n", int(size%int64(len(record))))); err != nil {
		slow.Close()
		return err
	}
	if err = buffered.Flush(); err != nil {
		slow.Close()
		return err
	}
	if err = slow.Close(); err != nil {
		return err
	}
	hostLine := fmt.Sprintf("{\"version\":1,\"at\":%d,\"environment\":\"large-check\",\"host\":%q,\"scope\":\"linux-host\",\"cpu\":{\"user\":100,\"system\":20,\"idle\":500,\"wait\":10,\"nice\":1,\"interrupt\":2,\"softirq\":3,\"steal\":0},\"memory\":{\"total\":10000,\"available\":8000}}\n", at, host)
	access := fmt.Sprintf("{\"timestamp\":%d,\"method\":\"GET\",\"uri\":\"/large-smoke\",\"status\":200,\"request_time\":0.125}\n", now.Unix())
	for name, text := range map[string]string{"host.jsonl": hostLine, "access.log": access} {
		if err = os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			return err
		}
	}
	pf, err := os.OpenFile(filepath.Join(dir, "fgprof.pprof"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	p := profile.Profile{SampleType: []*profile.ValueType{{Type: "samples", Unit: "count"}}}
	if err = p.Write(pf); err != nil {
		pf.Close()
		return err
	}
	if err = pf.Close(); err != nil {
		return err
	}
	files := []map[string]any{}
	for _, name := range []string{"host.jsonl", "access.log", "slow.log", "fgprof.pprof"} {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		files = append(files, map[string]any{"name": name, "bytes": n, "sha256": fmt.Sprintf("%x", h.Sum(nil))})
	}
	m := map[string]any{"version": 1, "capture_id": id, "environment": "large-check", "host": host, "started_at": at - 1000, "ended_at": at + 1000, "files": files}
	manifest, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0600); err != nil {
		return err
	}
	started := time.Now()
	if err = recorder.Upload(ctx, dir); err != nil {
		return err
	}
	if retry {
		if err = recorder.Upload(ctx, dir); err != nil {
			return err
		}
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "capture_id": id, "directory": dir, "slow_bytes": size, "expected_sql_count": records, "expected_sql_total_seconds": float64(records) * 0.1, "upload_seconds": time.Since(started).Seconds(), "retried": retry})
}
