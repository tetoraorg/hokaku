package hokaku

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRawLogCaptureAndRetry(t *testing.T) {
	uploaded := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		data, err := io.ReadAll(req.Body)
		if err != nil {
			t.Error(err)
		}
		uploaded[filepath.Base(req.URL.Path)] = data
	}))
	defer server.Close()
	c := testConfig(t, server.URL)
	path := filepath.Join(t.TempDir(), "pdns.log")
	if err := os.WriteFile(path, []byte("old records\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c.RawLogs = map[string]string{"powerdns": path}
	done := make(chan Result, 1)
	c.OnComplete = func(result Result) { done <- result }
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// New owns its configuration, including the map.
	c.RawLogs["powerdns"] = "/missing"
	if _, err := r.Start(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	const content = "unrecognized PowerDNS record\x00without newline"
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	var result Result
	select {
	case result = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("capture timed out")
	}
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if string(uploaded["raw-powerdns.log"]) != content {
		t.Fatalf("raw content: %q", uploaded["raw-powerdns.log"])
	}
	var m manifest
	if err := json.Unmarshal(uploaded["manifest.json"], &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 5 {
		t.Fatalf("manifest files: %v", m.Files)
	}
	// Retry relies on the manifest, not the recorder's current RawLogs settings.
	retryConfig := testConfig(t, server.URL)
	retryConfig.OutputDir = c.OutputDir
	retry, err := New(retryConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer retry.Close()
	if err := retry.Upload(t.Context(), result.Directory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(result.Directory, "raw-powerdns.log"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := retry.Upload(t.Context(), result.Directory); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered raw log: %v", err)
	}
}

func TestRawLogConfigValidation(t *testing.T) {
	for _, label := range []string{"", ".", "..", "../escape", "has/slash", "has space", strings.Repeat("a", 65)} {
		t.Run(label, func(t *testing.T) {
			c := testConfig(t, "http://127.0.0.1:1")
			c.RawLogs = map[string]string{label: "/tmp/log"}
			if _, err := New(c); err == nil {
				t.Fatal("accepted unsafe label")
			}
		})
	}
	c := testConfig(t, "http://127.0.0.1:1")
	c.RawLogs = map[string]string{"pdns": ""}
	if _, err := New(c); err == nil {
		t.Fatal("accepted empty path")
	}
}

func TestUploadRejectsUnsafeRawManifest(t *testing.T) {
	for _, name := range []string{"../secret", "manifest.json", "raw-../secret.log", "host.jsonl"} {
		t.Run(name, func(t *testing.T) {
			c := testConfig(t, "http://127.0.0.1:1")
			dir := filepath.Join(c.OutputDir, "capture-1")
			fakeCompleted(t, dir)
			data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var m manifest
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			m.Files = append(m.Files, fileEntry{Name: name})
			data, err = json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			r, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if err := r.Upload(t.Context(), dir); err == nil || !strings.Contains(err.Error(), "invalid manifest artifact") {
				t.Fatalf("unsafe manifest: %v", err)
			}
		})
	}
}
