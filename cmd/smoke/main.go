// Smoke runs synthetic log traffic with a real fgprof/gopsutil hokaku.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/tetoraorg/hokaku"
	"log"
	"os"
	"path/filepath"
	"time"
)

func main() {
	endpoint := flag.String("endpoint", "http://127.0.0.1:3000", "isolated test server")
	out := flag.String("out", "", "new private test directory")
	host := flag.String("host", "app-1", "test host name")
	captureID := flag.String("capture-id", "", "shared capture identifier")
	compressUploads := flag.Bool("compress-uploads", false, "send artifacts with zstd")
	flag.Parse()
	if *out == "" {
		log.Fatal("--out required")
	}
	if err := os.Mkdir(*out, 0700); err != nil {
		log.Fatal(err)
	}
	access := filepath.Join(*out, "nginx.log")
	slow := filepath.Join(*out, "mysql.log")
	done := make(chan hokaku.Result, 1)
	recorder, err := hokaku.New(hokaku.Config{Endpoint: *endpoint, Token: os.Getenv("KOSAKANA_ACCESS_KEY"), Environment: "record-check", Host: *host, OutputDir: filepath.Join(*out, "captures"), AccessLog: access, SlowLog: slow, Duration: 2 * time.Second, Interval: 200 * time.Millisecond, CompressUploads: *compressUploads, OnComplete: func(r hokaku.Result) { done <- r }, Rotate: func(context.Context) error {
		if err := os.WriteFile(access, nil, 0600); err != nil {
			return err
		}
		return os.WriteFile(slow, nil, 0600)
	}})
	if err != nil {
		log.Fatal(err)
	}
	defer recorder.Close()
	if *captureID == "" {
		_, err = recorder.Start()
	} else {
		_, err = recorder.StartWithID(*captureID)
	}
	if err != nil {
		log.Fatal(err)
	}
	if err := func() error {
		now := time.Now()
		lines := []byte(fmt.Sprintf("{\"timestamp\":%.3f,\"method\":\"GET\",\"uri\":\"/test\",\"status\":200,\"cookie\":\"app_session=synthetic-session\",\"user_agent\":\"kosakana-smoke\",\"request_time\":0.125}\n", float64(now.UnixMilli())/1000))
		if *host == "app-1" {
			marker := []byte(fmt.Sprintf("{\"timestamp\":%.3f,\"method\":\"POST\",\"uri\":\"/api/initialize\",\"status\":200,\"cookie\":\"app_session=synthetic-session\",\"user_agent\":\"kosakana-smoke\",\"request_time\":0.01}\n", float64(now.UnixMilli())/1000))
			lines = append(marker, lines...)
		}
		if err := os.WriteFile(access, lines, 0600); err != nil {
			return err
		}
		return os.WriteFile(slow, []byte(fmt.Sprintf("# Time: %s\n# User@Host: root[root] @ localhost []\n# Query_time: 0.100000  Lock_time: 0.000000 Rows_sent: 1  Rows_examined: 1\nSET timestamp=%.3f;\nSELECT 42;\n", now.UTC().Format(time.RFC3339Nano), float64(now.UnixMilli())/1000)), 0600)
	}(); err != nil {
		log.Fatal(err)
	}
	result := <-done
	if result.Err != nil {
		log.Fatal(result.Err)
	}
	// Repeat the same upload to exercise the receiver's idempotency.
	if err = recorder.Upload(context.Background(), result.Directory); err != nil {
		log.Fatal(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "capture_id": result.ID, "directory": result.Directory})
}
