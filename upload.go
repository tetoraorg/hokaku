package capture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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
	var total int64
	for _, declared := range m.Files {
		name := declared.Name
		if seen[name] || (!slices.Contains(artifactNames, name) && !isRawArtifact(name)) {
			return errors.New("invalid manifest artifact name")
		}
		seen[name] = true
		entry, err := describe(filepath.Join(dir, name), name)
		if err != nil {
			return err
		}
		if entry != declared {
			return errors.New("artifact checksum mismatch")
		}
		total += entry.Bytes
		names = append(names, name)
	}
	for _, name := range artifactNames {
		if !seen[name] {
			return errors.New("missing required artifact")
		}
	}
	if total > maxCaptureBytes {
		return errors.New("capture exceeds 128 MiB")
	}
	for _, name := range append(names, "manifest.json") {
		target := strings.TrimRight(r.config.Endpoint, "/") + "/api/captures/" + url.PathEscape(m.CaptureID) + "/" + url.PathEscape(m.Host) + "/" + name
		var last error
		for attempt := range 3 {
			f, err := os.Open(filepath.Join(dir, name))
			if err != nil {
				return err
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, f)
			if err != nil {
				f.Close()
				return err
			}
			req.Header.Set("Authorization", "Bearer "+r.config.Token)
			req.Header.Set("Content-Type", "application/octet-stream")
			response, err := r.client.Do(req)
			_ = f.Close()
			if err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
				_ = response.Body.Close()
				if response.StatusCode >= 200 && response.StatusCode < 300 {
					last = nil
					break
				}
				last = fmt.Errorf("upload %s: HTTP %d", name, response.StatusCode)
				if response.StatusCode < 500 {
					return last
				}
			} else {
				last = fmt.Errorf("upload %s failed: %w", name, err)
			}
			if attempt < 2 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(attempt+1) * time.Second):
				}
			}
		}
		if last != nil {
			return last
		}
	}
	return nil
}
