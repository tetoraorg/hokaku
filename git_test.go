package hokaku

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitTestRepo(t *testing.T) (string, func(...string) string) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-m", "Improve app")
	return dir, git
}

func TestGitMetadataDetectsUncommittedChanges(t *testing.T) {
	for _, change := range []string{"clean", "unstaged", "staged", "untracked", "ignored"} {
		t.Run(change, func(t *testing.T) {
			dir, git := gitTestRepo(t)
			switch change {
			case "unstaged", "staged":
				if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("changed\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if change == "staged" {
					git("add", ".")
				}
			case "untracked", "ignored":
				if err := os.WriteFile(filepath.Join(dir, "extra"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				if change == "ignored" {
					if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("extra\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(previous)
			info, err := readGitMetadata(dir)
			if err != nil {
				t.Fatal(err)
			}
			dirty := change != "clean" && change != "ignored"
			if info.Commit != git("rev-parse", "HEAD") || info.Dirty != dirty {
				t.Fatalf("unexpected metadata: %+v", info)
			}
			if strings.HasPrefix(info.Message, "[+]") != dirty || strings.Contains(logs.String(), "level=WARN") != dirty {
				t.Fatalf("dirty marker or warning missing: %+v, %s", info, logs.String())
			}
			if !dirty && info.Message != "Improve app" {
				t.Fatalf("unexpected clean message: %s", info.Message)
			}
		})
	}
}

func TestGitCollectionFailureDoesNotStartCapture(t *testing.T) {
	c := testConfig(t, "http://127.0.0.1:1")
	c.GitDir = t.TempDir()
	rotated := false
	c.Rotate = func(_ context.Context) error { rotated = true; return nil }
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.Start(); err == nil || rotated || profileActive.Load() {
		t.Fatalf("Git failure started capture or leaked profiler: %v", err)
	}
}

func TestCaptureUploadsGitSnapshotAndRetriesItUnchanged(t *testing.T) {
	dir, git := gitTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "extra"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	var uploaded []manifest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if filepath.Base(req.URL.Path) == "manifest.json" {
			var m manifest
			if err := json.NewDecoder(req.Body).Decode(&m); err != nil {
				t.Error(err)
			}
			uploaded = append(uploaded, m)
		} else {
			_, _ = io.Copy(io.Discard, req.Body)
		}
	}))
	defer server.Close()
	c := testConfig(t, server.URL)
	c.GitDir = dir
	done := make(chan Result, 1)
	c.OnComplete = func(result Result) { done <- result }
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.Start(); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-m", "Later commit")
	result := <-done
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if err := r.Upload(t.Context(), result.Directory); err != nil {
		t.Fatal(err)
	}
	if len(uploaded) != 2 || uploaded[0].Git == nil || *uploaded[0].Git != *uploaded[1].Git {
		t.Fatalf("Git snapshot changed on retry: %+v", uploaded)
	}
	info := uploaded[0].Git
	if !info.Dirty || info.Message != "[+]Improve app" || info.Commit == git("rev-parse", "HEAD") {
		t.Fatalf("capture did not retain start-time metadata: %+v", info)
	}
}
