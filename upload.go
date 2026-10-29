package hokaku

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Upload retries a completed local capture. Original files are never removed.
func (r *Recorder) Upload(ctx context.Context, dir string) error {
	root, err := filepath.EvalSymlinks(r.config.OutputDir)
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	directoryInfo, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("capture directory must not be a symlink")
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return err
	}
	if filepath.Dir(resolved) != root {
		return errors.New("capture directory is outside OutputDir")
	}
	dir = resolved
	manifestPath := filepath.Join(dir, "manifest.json")
	info, err := os.Lstat(manifestPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 65536 {
		return errors.New("invalid manifest file")
	}
	manifestFile, err := os.Open(manifestPath)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(manifestFile, 65537))
	err = errors.Join(err, manifestFile.Close())
	if err != nil {
		return err
	}
	if len(data) > 65536 {
		return errors.New("manifest too large")
	}
	var m manifest
	if err = json.Unmarshal(data, &m); err != nil {
		return err
	}
	if m.Version != 1 || !validName.MatchString(m.CaptureID) || m.CaptureID == "." || m.CaptureID == ".." || filepath.Base(dir) != m.CaptureID || m.Host != r.config.Host || m.Environment != r.config.Environment || (len(m.Files) < len(artifactNames) || len(m.Files) > len(artifactNames)+maxRawLogs) {
		return errors.New("manifest identity mismatch")
	}
	names := make([]string, 0, len(m.Files)+1)
	seen := make(map[string]bool)
	for _, declared := range m.Files {
		name := declared.Name
		if seen[name] || (!slices.Contains(artifactNames, name) && !isRawArtifact(name)) {
			return errors.New("invalid manifest artifact name")
		}
		seen[name] = true
		entry, err := describeContext(ctx, filepath.Join(dir, name), name)
		if err != nil {
			return err
		}
		if entry != declared {
			return errors.New("artifact checksum mismatch")
		}
		names = append(names, name)
	}
	for _, name := range artifactNames {
		if !seen[name] {
			return errors.New("missing required artifact")
		}
	}
	for _, name := range append(names, "manifest.json") {
		target := strings.TrimRight(r.config.Endpoint, "/") + "/api/captures/" + url.PathEscape(m.CaptureID) + "/" + url.PathEscape(m.Host) + "/" + name
		var entry fileEntry
		for _, declared := range m.Files {
			if declared.Name == name {
				entry = declared
				break
			}
		}
		if err := r.uploadFile(ctx, filepath.Join(dir, name), target, entry); err != nil {
			return fmt.Errorf("upload %s: %w", name, err)
		}
	}
	return nil
}

// UploadChunkBytes bounds each HTTP request, not the size of a capture or file.
// Keep in sync with the chunks-v1 receiver contract.
const UploadChunkBytes int64 = 32 << 20

func (r *Recorder) uploadFile(ctx context.Context, path, target string, entry fileEntry) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("artifact must be a regular file")
	}
	total := info.Size()
	chunked := entry.Bytes > UploadChunkBytes
	if entry.Name != "" && total != entry.Bytes {
		return errors.New("artifact size changed")
	}
	if chunked {
		target += ".chunks"
	}
	for offset := int64(0); ; {
		length := total - offset
		if chunked && length > UploadChunkBytes {
			length = UploadChunkBytes
		}
		headers := make(http.Header)
		if chunked {
			hash := sha256.New()
			if _, err := io.Copy(hash, contextReader{ctx, io.NewSectionReader(f, offset, length)}); err != nil {
				return err
			}
			headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, total))
			headers.Set("X-Kosakana-File-SHA256", entry.SHA256)
			headers.Set("X-Kosakana-Chunk-SHA256", fmt.Sprintf("%x", hash.Sum(nil)))
		}
		next, err := r.uploadSection(ctx, target, f, offset, length, total, chunked, headers)
		if err != nil {
			return err
		}
		if !chunked || next == total {
			return nil
		}
		offset = next
	}
}

func (r *Recorder) uploadSection(ctx context.Context, target string, f *os.File, offset, length, total int64, chunked bool, headers http.Header) (int64, error) {
	var last error
	for attempt := range 3 {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		body := io.NopCloser(io.NewSectionReader(f, offset, length))
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, body)
		if err != nil {
			return 0, err
		}
		req.ContentLength = length
		if length == 0 {
			req.Body = http.NoBody
		}
		req.Header = headers.Clone()
		req.Header.Set("Authorization", "Bearer "+r.config.Token)
		req.Header.Set("Content-Type", "application/octet-stream")
		response, err := r.client.Do(req)
		if err == nil {
			_, drainErr := io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				if !chunked {
					return total, nil
				}
				if response.StatusCode != 200 && response.StatusCode != 201 {
					return 0, errors.New("invalid chunk acknowledgement status")
				}
				if response.Header.Get("X-Kosakana-Upload-Protocol") != "chunks-v1" {
					return 0, errors.New("receiver does not acknowledge chunks-v1; update kosakana first")
				}
				value := response.Header.Get("X-Kosakana-Upload-Offset")
				next, parseErr := strconv.ParseInt(value, 10, 64)
				if parseErr != nil || strconv.FormatInt(next, 10) != value || next < offset+length || next > total || (next != total && next%UploadChunkBytes != 0) {
					return 0, errors.New("invalid chunk acknowledgement offset")
				}
				if drainErr == nil {
					return next, nil
				}
				last = drainErr
			} else {
				last = fmt.Errorf("HTTP %d", response.StatusCode)
				if response.StatusCode < 500 {
					return 0, last
				}
			}
		} else {
			last = err
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * time.Second):
			}
		}
	}
	return 0, last
}
