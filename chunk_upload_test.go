package hokaku

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sparseArtifact(t *testing.T, size int64) (string, fileEntry) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "slow.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	// Distinct markers expose incorrect file offsets even in a sparse fixture.
	for offset := int64(0); offset < size; offset += UploadChunkBytes {
		if _, err = f.WriteAt([]byte{byte(offset/UploadChunkBytes + 1)}, offset); err != nil {
			t.Fatal(err)
		}
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	entry, err := describe(path, "slow.log")
	if err != nil {
		t.Fatal(err)
	}
	return path, entry
}

func acknowledge(w http.ResponseWriter, offset int64) {
	w.Header().Set("X-Kosakana-Upload-Protocol", "chunks-v1")
	w.Header().Set("X-Kosakana-Upload-Offset", strconv.FormatInt(offset, 10))
	w.WriteHeader(http.StatusCreated)
}

func TestChunkUploadOverOneGiB(t *testing.T) {
	// A sparse source and streaming receiver exercise every byte without a GiB allocation.
	const total = (1 << 30) + 17
	path, entry := sparseArtifact(t, total)
	var offset int64
	var requests int
	fullHash := sha256.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/slow.log.chunks" {
			t.Errorf("path: %s", r.URL.Path)
		}
		length := min(UploadChunkBytes, total-offset)
		want := fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, total)
		if r.Header.Get("Content-Range") != want || r.ContentLength != length {
			t.Errorf("range or length: %s %d", r.Header.Get("Content-Range"), r.ContentLength)
		}
		if r.Header.Get("Authorization") != "Bearer test" || r.Header.Get("X-Kosakana-File-SHA256") != entry.SHA256 {
			t.Error("missing identity")
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(h, fullHash), r.Body)
		if err != nil || n != length {
			t.Errorf("read: %d %v", n, err)
		}
		if fmt.Sprintf("%x", h.Sum(nil)) != r.Header.Get("X-Kosakana-Chunk-SHA256") {
			t.Error("chunk checksum")
		}
		offset += n
		requests++
		acknowledge(w, offset)
	}))
	defer server.Close()
	r := &Recorder{config: Config{Token: "test"}, client: server.Client()}
	if err := r.uploadFile(t.Context(), path, server.URL+"/slow.log", entry); err != nil {
		t.Fatal(err)
	}
	if offset != total || requests != 33 || fmt.Sprintf("%x", fullHash.Sum(nil)) != entry.SHA256 {
		t.Fatalf("incomplete transfer: %d bytes, %d requests", offset, requests)
	}
}

func TestChunkResumeSkipsDurablePrefixAndRetriesSameChunk(t *testing.T) {
	path, entry := sparseArtifact(t, 3*UploadChunkBytes+7)
	var ranges []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranges = append(ranges, r.Header.Get("Content-Range"))
		_, _ = io.Copy(io.Discard, r.Body)
		switch len(ranges) {
		case 1:
			acknowledge(w, 3*UploadChunkBytes) // already received before interruption
		case 2:
			w.WriteHeader(503)
		default:
			acknowledge(w, entry.Bytes)
		}
	}))
	defer server.Close()
	r := &Recorder{client: server.Client()}
	if err := r.uploadFile(t.Context(), path, server.URL+"/slow.log", entry); err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 3 || ranges[1] != ranges[2] || ranges[0] == ranges[1] {
		t.Fatalf("ranges: %v", ranges)
	}
}

func TestChunkRejectsInvalidAcknowledgements(t *testing.T) {
	path, entry := sparseArtifact(t, 2*UploadChunkBytes+1)
	for _, test := range []struct {
		name, protocol, offset string
		status                 int
	}{
		{"old receiver", "", "", 400},
		{"silent legacy success", "", "", 201},
		{"missing offset", "chunks-v1", "", 201},
		{"backwards", "chunks-v1", "0", 200},
		{"past EOF", "chunks-v1", strconv.FormatInt(entry.Bytes+1, 10), 201},
		{"unaligned", "chunks-v1", strconv.FormatInt(UploadChunkBytes+1, 10), 200},
		{"false accepted", "chunks-v1", strconv.FormatInt(entry.Bytes, 10), 202},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if !strings.HasSuffix(r.URL.Path, ".chunks") {
					t.Error("fell back to large ordinary request")
				}
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("X-Kosakana-Upload-Protocol", test.protocol)
				w.Header().Set("X-Kosakana-Upload-Offset", test.offset)
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			r := &Recorder{client: server.Client()}
			if err := r.uploadFile(t.Context(), path, server.URL+"/slow.log", entry); err == nil {
				t.Fatal("accepted invalid acknowledgement")
			}
			if requests != 1 {
				t.Fatalf("requests: %d", requests)
			}
		})
	}
}

func TestChunkCancellationWhileWaitingForAcknowledgement(t *testing.T) {
	path, entry := sparseArtifact(t, UploadChunkBytes+1)
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	r := &Recorder{client: server.Client()}
	done := make(chan error, 1)
	go func() { done <- r.uploadFile(ctx, path, server.URL+"/slow.log", entry) }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("upload stopped before request: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upload failed to cancel")
	}
}

func TestHTTPClientHasNoImplicitTransferDeadline(t *testing.T) {
	for _, timeout := range []time.Duration{0, 5 * time.Minute} {
		c := testConfig(t, "http://127.0.0.1:1")
		c.HTTPClient = &http.Client{Timeout: timeout}
		r, err := New(c)
		if err != nil {
			t.Fatal(err)
		}
		if r.client.Timeout != timeout {
			t.Fatalf("timeout: %v", r.client.Timeout)
		}
		r.Close()
	}
	c := testConfig(t, "http://127.0.0.1:1")
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.client.Timeout != 0 {
		t.Fatalf("default timeout: %v", r.client.Timeout)
	}
}

func TestCancelledUploadDoesNotScanWholeArtifact(t *testing.T) {
	path, _ := sparseArtifact(t, UploadChunkBytes+1)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := describeContext(ctx, path, "slow.log")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error: %v", err)
	}
}

func TestSmallAndEmptyFilesKeepOrdinaryProtocol(t *testing.T) {
	for _, size := range []int64{0, 17, UploadChunkBytes} {
		path, entry := sparseArtifact(t, size)
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.URL.Path != "/slow.log" || r.Header.Get("Content-Range") != "" || r.ContentLength != size {
				t.Errorf("request: %s %d", r.URL.Path, r.ContentLength)
			}
			n, err := io.Copy(io.Discard, r.Body)
			if err != nil || n != size {
				t.Errorf("read: %d %v", n, err)
			}
			w.WriteHeader(201)
		}))
		r := &Recorder{client: server.Client()}
		err := r.uploadFile(t.Context(), path, server.URL+"/slow.log", entry)
		server.Close()
		if err != nil || calls.Load() != 1 {
			t.Fatalf("upload: %v %d", err, calls.Load())
		}
	}
}

func TestUploadLargeCapturePublishesManifestAfterChunks(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, filepath.Base(r.URL.Path))
		_, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			t.Error(err)
		}
		if strings.HasSuffix(r.URL.Path, ".chunks") {
			var start, end, total int64
			if _, err := fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err != nil {
				t.Error(err)
			}
			acknowledge(w, end+1)
		} else {
			w.WriteHeader(201)
		}
	}))
	defer server.Close()
	c := testConfig(t, server.URL)
	dir := filepath.Join(c.OutputDir, "large-capture")
	fakeCompleted(t, dir)
	if err := os.Truncate(filepath.Join(dir, "slow.log"), UploadChunkBytes+1); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(dir, "large-capture", c.Environment, c.Host, time.Now(), time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err = r.Upload(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	want := "host.jsonl,access.log,slow.log.chunks,slow.log.chunks,fgprof.pprof,manifest.json"
	if strings.Join(paths, ",") != want {
		t.Fatalf("paths: %v", paths)
	}
}
