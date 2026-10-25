package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRunUploadsCapture(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("unexpected upload method or authentication")
		}
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	getenv := func(name string) string {
		switch name {
		case "KOSAKANA_ENDPOINT":
			return server.URL
		case "KOSAKANA_ACCESS_KEY":
			return "test-token"
		}
		return ""
	}
	out := t.TempDir()
	var output bytes.Buffer
	err := run(t.Context(), []string{"--environment", "test", "--host", "app-1", "--output-dir", out, "--capture-id", "cli-test", "--duration", "300ms", "--interval", "100ms"}, getenv, &output)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 5 || paths[4] != "/api/captures/cli-test/app-1/manifest.json" {
		t.Fatalf("unexpected upload order: %v", paths)
	}
	if _, err := os.Stat(filepath.Join(out, "cli-test", "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "capture uploaded: cli-test") {
		t.Fatalf("missing completion: %s", &output)
	}
}

func TestRunRejectsLogsWithoutRotation(t *testing.T) {
	err := run(t.Context(), []string{"--access-log", "/unused.log"}, func(string) string { return "" }, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "rotation command") {
		t.Fatalf("expected rotation requirement, got %v", err)
	}
}

func TestRunCanceledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out := filepath.Join(t.TempDir(), "not-created")
	err := run(ctx, []string{"--endpoint", "http://127.0.0.1:1", "--environment", "test", "--host", "app-1", "--output-dir", out}, func(string) string { return "test-token" }, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected output directory: %v", err)
	}
}

func TestRunHelp(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), []string{"--help"}, func(string) string { return "" }, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "KOSAKANA_ACCESS_KEY") {
		t.Fatal("help is missing authentication instructions")
	}
}

func TestRunRotationFailure(t *testing.T) {
	err := run(t.Context(), []string{
		"--endpoint", "http://127.0.0.1:1", "--environment", "test", "--host", "app-1",
		"--output-dir", t.TempDir(), "--access-log", "/unused.log",
		"--", "/bin/sh", "-c", "exit 23",
	}, func(string) string { return "test-token" }, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "exit status 23") {
		t.Fatalf("expected rotation command failure, got %v", err)
	}
}

func TestRunCanceledDuringUpload(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		cancel()
		<-r.Context().Done()
	}))
	defer server.Close()
	err := run(ctx, []string{
		"--endpoint", server.URL, "--environment", "test", "--host", "app-1",
		"--output-dir", t.TempDir(), "--duration", "300ms", "--interval", "100ms",
	}, func(string) string { return "test-token" }, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected upload cancellation, got %v", err)
	}
}
