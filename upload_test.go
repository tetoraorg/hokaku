package hokaku

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeCompleted(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	m := manifest{Version: 1, CaptureID: filepath.Base(dir), Environment: "test", Host: "app-1", StartedAt: 1, EndedAt: 2}
	for _, name := range artifactNames {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("private content\n"), 0600); err != nil {
			t.Fatal(err)
		}
		entry, err := describe(filepath.Join(dir, name), name)
		if err != nil {
			t.Fatal(err)
		}
		m.Files = append(m.Files, entry)
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestUploadRejectsOutsideDirectory(t *testing.T) {
	c := testConfig(t, "http://127.0.0.1:1")
	if err := os.MkdirAll(c.OutputDir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	fakeCompleted(t, outside)
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err = r.Upload(t.Context(), outside); err == nil || !strings.Contains(err.Error(), "outside OutputDir") {
		t.Fatalf("unexpected error: %v", err)
	}
}
func TestUploadRejectsArtifactAndDirectorySymlinks(t *testing.T) {
	for _, which := range []string{"artifact", "directory", "manifest"} {
		t.Run(which, func(t *testing.T) {
			c := testConfig(t, "http://127.0.0.1:1")
			dir := filepath.Join(c.OutputDir, "capture-1")
			fakeCompleted(t, dir)
			var original, link string
			switch which {
			case "artifact":
				link = filepath.Join(dir, "host.jsonl")
				original = link + ".real"
			case "manifest":
				link = filepath.Join(dir, "manifest.json")
				original = link + ".real"
			case "directory":
				link = dir
				original = dir + "-real"
			}
			if err := os.Rename(link, original); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(original, link); err != nil {
				t.Fatal(err)
			}
			r, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if err = r.Upload(t.Context(), dir); err == nil || (!strings.Contains(err.Error(), "symlink") && !strings.Contains(err.Error(), "invalid manifest")) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestManifestPublicationDoesNotOverwriteExistingMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishManifest(dir, manifest{Version: 1}); err == nil {
		t.Fatal("overwrote marker")
	}
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != "original" {
		t.Fatal("changed marker")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatal("left pending manifest")
	}
}

func TestDescribeAcceptsArtifactBeyond128MiB(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "large-artifact-*")
	if err != nil {
		t.Fatal(err)
	}
	path := file.Name()
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Truncate(path, (128<<20)+1); err != nil {
		t.Fatal(err)
	}

	entry, err := describe(path, "large-artifact")
	if err != nil || entry.Bytes != (128<<20)+1 {
		t.Fatalf("describe() = %v, %v, want complete artifact", entry, err)
	}
}

func TestDescribeAcceptsArtifactAbove32MiB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, (32<<20)+1); err != nil {
		t.Fatal(err)
	}
	entry, err := describe(path, "access.log")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Bytes != (32<<20)+1 {
		t.Fatalf("bytes = %d", entry.Bytes)
	}
}

func TestManifestAcceptsMoreThan512MiB(t *testing.T) {
	dir := t.TempDir()
	for _, name := range artifactNames {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(path, 128<<20); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeManifest(dir, "large", "test", "app-1", time.Now(), time.Now().UnixMilli(), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw-extra.log"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(dir, "large", "test", "app-1", time.Now(), time.Now().UnixMilli(), nil, "raw-extra.log"); err != nil {
		t.Fatalf("writeManifest() failed: %v", err)
	}
}
