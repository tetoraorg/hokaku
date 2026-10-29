package hokaku

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	capturefgprof "github.com/tetoraorg/hokaku/internal/fgprof"
	capturemysql "github.com/tetoraorg/hokaku/internal/mysql"
	capturenginx "github.com/tetoraorg/hokaku/internal/nginx"
)

const maxRawLogs = 16

var validRawLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func rawArtifactName(label string) string { return "raw-" + label + ".log" }

func isRawArtifact(name string) bool {
	label, ok := strings.CutPrefix(name, "raw-")
	if !ok {
		return false
	}
	label, ok = strings.CutSuffix(label, ".log")
	return ok && validRawLabel.MatchString(label)
}

var artifactNames = []string{hostJSONLName, capturenginx.ArtifactName, capturemysql.ArtifactName, capturefgprof.ProfileName}

type fileEntry struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}
type manifest struct {
	Version     int         `json:"version"`
	CaptureID   string      `json:"capture_id"`
	Environment string      `json:"environment"`
	Host        string      `json:"host"`
	StartedAt   int64       `json:"started_at"`
	EndedAt     int64       `json:"ended_at"`
	Files       []fileEntry `json:"files"`
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func describe(path, name string) (fileEntry, error) {
	return describeContext(context.Background(), path, name)
}

func describeContext(ctx context.Context, path, name string) (fileEntry, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return fileEntry{}, err
	}
	if !before.Mode().IsRegular() {
		return fileEntry{}, errors.New("artifact must be a regular file, not a symlink")
	}
	f, err := os.Open(path)
	if err != nil {
		return fileEntry{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fileEntry{}, err
	}
	if !os.SameFile(before, info) || !info.Mode().IsRegular() {
		return fileEntry{}, errors.New("invalid artifact")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, contextReader{ctx, f})
	if err != nil {
		return fileEntry{}, err
	}
	return fileEntry{name, n, hex.EncodeToString(hash.Sum(nil))}, nil
}

// writeManifest hashes the complete artifact set and publishes only a fully
// synced completion marker. A crash may leave a pending file, never a partial
// manifest.json.
func writeManifest(dir, id, environment, host string, started time.Time, endedAt int64, rawNames ...string) error {
	m := manifest{
		Version:     1,
		CaptureID:   id,
		Environment: environment,
		Host:        host,
		StartedAt:   started.UnixMilli(),
		EndedAt:     endedAt,
	}
	for _, name := range append(append([]string{}, artifactNames...), rawNames...) {
		entry, err := describe(filepath.Join(dir, name), name)
		if err != nil {
			return err
		}
		m.Files = append(m.Files, entry)
	}
	return publishManifest(dir, m)
}

func publishManifest(dir string, m manifest) error {
	f, err := os.CreateTemp(dir, ".manifest-*.pending")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(m)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	// A hard link publishes atomically and refuses an existing final name.
	return os.Link(f.Name(), filepath.Join(dir, "manifest.json"))
}
