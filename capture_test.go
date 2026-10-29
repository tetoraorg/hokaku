package hokaku

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/pprof/profile"
)

func testConfig(t *testing.T, endpoint string) Config {
	t.Helper()
	dir := t.TempDir()
	access := filepath.Join(dir, "nginx.log")
	slow := filepath.Join(dir, "mysql.log")
	return Config{Endpoint: endpoint, Token: "test-token", Environment: "test", Host: "app-1", OutputDir: filepath.Join(dir, "captures"), AccessLog: access, SlowLog: slow, Duration: 400 * time.Millisecond, Interval: 100 * time.Millisecond, Rotate: func(context.Context) error {
		if err := os.WriteFile(access, []byte("{\"timestamp\":1,\"method\":\"GET\",\"uri\":\"/\",\"status\":200,\"request_time\":0}\n"), 0600); err != nil {
			return err
		}
		return os.WriteFile(slow, nil, 0600)
	}}
}
func TestCaptureUploadsManifestLastAndPreservesArtifacts(t *testing.T) {
	var mu sync.Mutex
	var names []string
	uploaded := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing token")
		}
		b, err := io.ReadAll(req.Body)
		if err != nil {
			t.Error(err)
		}
		name := filepath.Base(req.URL.Path)
		mu.Lock()
		names = append(names, name)
		uploaded[name] = b
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer server.Close()
	c := testConfig(t, server.URL)
	done := make(chan Result, 1)
	c.OnComplete = func(r Result) { done <- r }
	recorder, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	id, err := recorder.Start()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recorder.Start(); !errors.Is(err, ErrBusy) {
		t.Fatalf("overlap: %v", err)
	}
	var result Result
	select {
	case result = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("capture did not finish")
	}
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.ID != id {
		t.Fatal("ID changed")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(names) != 5 || names[4] != "manifest.json" {
		t.Fatalf("wrong upload order: %v", names)
	}
	var manifest manifest
	if err = json.Unmarshal(uploaded["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.EndedAt-manifest.StartedAt < 350 {
		t.Fatal("ended too early")
	}
	for _, entry := range manifest.Files {
		actual, err := describe(filepath.Join(result.Directory, entry.Name), entry.Name)
		if err != nil || actual != entry {
			t.Fatalf("artifact mismatch: %v", err)
		}
	}
	if _, err = profile.ParseData(uploaded["fgprof.pprof"]); err != nil {
		t.Fatalf("invalid fgprof: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(uploaded["host.jsonl"])), "\n")
	if len(lines) < 3 {
		t.Fatalf("not enough samples: %d", len(lines))
	}
	var last int64
	processScans := 0
	for _, line := range lines {
		var s hostSample
		if err = json.Unmarshal([]byte(line), &s); err != nil {
			t.Fatal(err)
		}
		if s.At <= last || s.Memory.Total == 0 || s.Memory.Available > s.Memory.Total {
			t.Fatal("invalid sample")
		}
		if s.Processes != nil {
			processScans++
			if s.Processes.At < s.At || s.Processes.Items == nil {
				t.Fatal("invalid process scan")
			}
		}
		last = s.At
	}
	if processScans != 1 {
		t.Fatalf("process interval ignored: %d scans in 400ms", processScans)
	}
}
func TestRotationFailureDoesNotStartWorker(t *testing.T) {
	c := testConfig(t, "http://127.0.0.1:1")
	c.Rotate = func(context.Context) error { return errors.New("cannot reopen") }
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err = r.Start(); err == nil {
		t.Fatal("accepted failed rotation")
	}
	if profileActive.Load() {
		t.Fatal("profiler lock leaked")
	}
}
func TestCancellationDoesNotUpload(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	c := testConfig(t, server.URL)
	c.Duration = time.Minute
	done := make(chan Result, 1)
	c.OnComplete = func(r Result) { done <- r }
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Start()
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	result := <-done
	if result.Err == nil || calls != 0 {
		t.Fatalf("canceled capture was uploaded: %v, %d", result.Err, calls)
	}
	if _, err = os.Stat(filepath.Join(result.Directory, "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled capture has manifest")
	}
}
func TestRejectRemoteHTTPAndMissingRotation(t *testing.T) {
	c := testConfig(t, "http://example.com")
	if _, err := New(c); err == nil {
		t.Fatal("remote HTTP accepted")
	}
	c.Endpoint = "http://127.0.0.1:3000"
	c.Rotate = nil
	if _, err := New(c); err == nil {
		t.Fatal("missing rotation accepted")
	}
}
func TestFailedUploadRetainsManifest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }))
	defer server.Close()
	c := testConfig(t, server.URL)
	done := make(chan Result, 1)
	c.OnComplete = func(r Result) { done <- r }
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err = r.Start(); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.Err == nil {
		t.Fatal("401 treated as success")
	}
	if _, err = os.Stat(filepath.Join(result.Directory, "manifest.json")); err != nil {
		t.Fatal("lost retry manifest")
	}
}

func TestStartWithIDUsesSuppliedCaptureID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if _, err := io.Copy(io.Discard, req.Body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := testConfig(t, server.URL)
	done := make(chan Result, 1)
	c.OnComplete = func(result Result) { done <- result }
	recorder, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()

	const captureID = "shared-capture-01"
	id, err := recorder.StartWithID(captureID)
	if err != nil {
		t.Fatal(err)
	}
	if id != captureID {
		t.Fatalf("StartWithID returned %q, want %q", id, captureID)
	}

	select {
	case result := <-done:
		if result.Err != nil {
			t.Fatal(result.Err)
		}
		if result.ID != captureID {
			t.Fatalf("completion ID %q, want %q", result.ID, captureID)
		}
		if result.Directory != filepath.Join(c.OutputDir, captureID) {
			t.Fatalf("capture directory %q does not use supplied ID", result.Directory)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("capture did not finish")
	}
}

func TestStartWithIDRejectsInvalidNames(t *testing.T) {
	for _, id := range []string{"", ".", "..", "../escape", "has/slash"} {
		t.Run(id, func(t *testing.T) {
			c := testConfig(t, "http://127.0.0.1:1")
			rotated := false
			c.Rotate = func(context.Context) error {
				rotated = true
				return nil
			}
			recorder, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			defer recorder.Close()
			if got, err := recorder.StartWithID(id); err == nil || got != "" {
				t.Fatalf("StartWithID(%q) = %q, %v; want rejection", id, got, err)
			}
			if rotated {
				t.Fatal("invalid capture ID rotated logs")
			}
			if profileActive.Load() {
				t.Fatal("invalid capture ID leaked profiler lock")
			}
		})
	}
}

func TestStartWithIDDoesNotReuseExistingDirectory(t *testing.T) {
	c := testConfig(t, "http://127.0.0.1:1")
	rotated := false
	c.Rotate = func(context.Context) error {
		rotated = true
		return nil
	}
	const captureID = "capture-already-exists"
	directory := filepath.Join(c.OutputDir, captureID)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(directory, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}

	recorder, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	if got, err := recorder.StartWithID(captureID); err == nil || got != "" {
		t.Fatalf("StartWithID(%q) = %q, %v; want existing-directory failure", captureID, got, err)
	}
	contents, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "preserve" {
		t.Fatalf("existing capture was modified: %q", contents)
	}
	if rotated {
		t.Fatal("existing capture directory rotated logs")
	}
	if profileActive.Load() {
		t.Fatal("existing capture failure leaked profiler lock")
	}
}

func TestProcessIntervalValidation(t *testing.T) {
	for _, interval := range []time.Duration{time.Millisecond, -time.Second, 11 * time.Second} {
		c := testConfig(t, "http://127.0.0.1:1")
		c.ProcessInterval = interval
		if _, err := New(c); err == nil {
			t.Fatalf("accepted %v", interval)
		}
	}
}
