// Package hokaku records an ISUCON application's fgprof profile, host metrics,
// and rotated server logs, then uploads them after the measurement window.
package hokaku

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	capturefgprof "github.com/tetoraorg/hokaku/internal/fgprof"
	capturelogs "github.com/tetoraorg/hokaku/internal/logs"
	capturemysql "github.com/tetoraorg/hokaku/internal/mysql"
	capturenginx "github.com/tetoraorg/hokaku/internal/nginx"
	captureprocess "github.com/tetoraorg/hokaku/internal/process"
)

var ErrBusy = errors.New("a capture is already running")
var profileActive atomic.Bool
var validName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// Config is immutable after New. Rotate must finish reopening the NGINX and
// MySQL logs before returning; it runs synchronously inside Start.
type Config struct {
	Endpoint, Token, Environment, Host, OutputDir string
	AccessLog, SlowLog                            string // Empty paths produce empty artifacts.
	// RawLogs maps a label to a file path. Only bytes appended after Start opens
	// the file are captured, without parsing or requiring a final newline.
	// DisableProcessMetrics disables the optional Linux process scan.
	DisableProcessMetrics bool
	// ProcessInterval defaults to 1s and must be between 1s and 10s. Scans run on host ticks.
	ProcessInterval    time.Duration
	RawLogs            map[string]string
	Rotate             func(context.Context) error
	Duration, Interval time.Duration // Defaults: 60 seconds and 1 second.
	OnComplete         func(Result)  // Called by the worker after recording/upload completes.
	HTTPClient         *http.Client
}
type Result struct {
	ID, Directory string
	Err           error
}
type Recorder struct {
	config Config
	client *http.Client
	mu     sync.Mutex
	cancel context.CancelFunc
	closed bool
	wg     sync.WaitGroup
}

func New(c Config) (*Recorder, error) {
	if !validName.MatchString(c.Host) || c.Host == "." || c.Host == ".." || !validName.MatchString(c.Environment) || len(c.Environment) > 64 || c.Environment == "." || c.Environment == ".." {
		return nil, errors.New("invalid host or environment")
	}
	if len(c.RawLogs) > maxRawLogs {
		return nil, errors.New("at most 16 raw logs are allowed")
	}
	for name, path := range c.RawLogs {
		if !validRawLabel.MatchString(name) || path == "" {
			return nil, errors.New("raw logs require a safe label and nonempty path")
		}
	}
	c.RawLogs = maps.Clone(c.RawLogs)
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("endpoint must be an origin URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
		return nil, errors.New("use HTTPS or a loopback SSH tunnel")
	}
	if c.Token == "" || c.OutputDir == "" || c.Rotate == nil {
		return nil, errors.New("token, output directory and log rotation callback are required")
	}
	if c.Duration == 0 {
		c.Duration = 60 * time.Second
	}
	if c.ProcessInterval == 0 {
		c.ProcessInterval = time.Second
	}
	if c.ProcessInterval < time.Second || c.ProcessInterval > 10*time.Second {
		return nil, errors.New("process interval must be between 1s and 10s")
	}
	if c.Interval == 0 {
		c.Interval = time.Second
	}
	if c.Interval < 100*time.Millisecond || c.Duration < 2*c.Interval || c.Duration > 4*time.Minute {
		return nil, errors.New("duration must be 2 intervals to 240 seconds; interval at least 100ms")
	}
	// Do not impose a wall-clock deadline on large uploads or receiver analysis.
	// The default transport still bounds connection setup; Close/Upload context cancels work.
	client := &http.Client{}
	if c.HTTPClient != nil {
		copy := *c.HTTPClient
		client = &copy
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("upload redirects are disabled") }
	return &Recorder{config: c, client: client}, nil
}

// Start rotates logs and starts profiling before returning. The 60-second
// recording and upload run in a goroutine, independently of the HTTP request's
// context. Call Start from initialize after resetting application state.
func (r *Recorder) Start() (string, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	id := fmt.Sprintf("%d-%s", time.Now().UnixMilli(), hex.EncodeToString(nonce))
	return r.StartWithID(id)
}

// StartWithID starts a capture with an ID shared by multiple application hosts.
// It rejects IDs that are unsafe as directory names and never reuses an existing directory.
func (r *Recorder) StartWithID(id string) (string, error) {
	if !validName.MatchString(id) || id == "." || id == ".." {
		return "", errors.New("invalid capture ID")
	}
	return r.start(id)
}

func (r *Recorder) start(id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return "", errors.New("recorder is closed")
	}
	if r.cancel != nil || !profileActive.CompareAndSwap(false, true) {
		return "", ErrBusy
	}
	ready := false
	defer func() {
		if !ready {
			profileActive.Store(false)
		}
	}()
	dir := filepath.Join(r.config.OutputDir, id)
	if err := os.MkdirAll(r.config.OutputDir, 0700); err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	rotateCtx, rotateCancel := context.WithTimeout(ctx, 10*time.Second)
	err := r.config.Rotate(rotateCtx)
	rotateCancel()
	if err != nil {
		cancel()
		return "", fmt.Errorf("rotate logs: %w", err)
	}
	targets := []capturelogs.Target{capturenginx.Target(r.config.AccessLog), capturemysql.Target(r.config.SlowLog)}
	for _, name := range slices.Sorted(maps.Keys(r.config.RawLogs)) {
		targets = append(targets, capturelogs.Target{SourcePath: r.config.RawLogs[name], ArtifactName: rawArtifactName(name), Raw: true})
	}
	logSnapshot, err := capturelogs.Open(targets...)
	if err != nil {
		cancel()
		return "", err
	}
	profile, err := capturefgprof.Start(dir)
	if err != nil {
		logSnapshot.Close()
		cancel()
		return "", err
	}
	started := time.Now()
	r.cancel = cancel
	ready = true
	r.wg.Go(func() {
		defer func() { r.mu.Lock(); r.cancel = nil; r.mu.Unlock(); cancel() }()
		err := r.record(ctx, dir, id, started, logSnapshot, profile)
		profileActive.Store(false)
		if err == nil {
			err = r.Upload(ctx, dir)
		}
		if r.config.OnComplete != nil {
			r.config.OnComplete(Result{ID: id, Directory: dir, Err: err})
		}
	})
	return id, nil
}

// Close cancels recording/upload and waits for cleanup. Canceled recordings
// keep their files but do not get a manifest or get uploaded.
func (r *Recorder) Close() {
	r.mu.Lock()
	r.closed = true
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *Recorder) record(ctx context.Context, dir, id string, started time.Time, logs *capturelogs.Snapshot, profile *capturefgprof.Session) (result error) {
	defer logs.Close()
	defer func() { result = errors.Join(result, profile.StopAndClose()) }()
	metrics, err := openHostSampleWriter(dir)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, metrics.close()) }()
	readers := defaultHostReaders(ctx)
	nextProcess := started
	sample := func() error {
		row, err := readHostSample(ctx, r.config.Environment, r.config.Host, readers)
		if err != nil {
			return err
		}
		if !r.config.DisableProcessMetrics && !time.Now().Before(nextProcess) {
			row.Processes = captureprocess.Read(ctx)
			// Advance the schedule, not the last observed tick: jitter must not
			// accidentally turn a 1s process interval into a 2s interval.
			for !nextProcess.After(time.Now()) {
				nextProcess = nextProcess.Add(r.config.ProcessInterval)
			}
		}
		if err = logs.Check(); err != nil {
			return err
		}
		return metrics.write(row)
	}
	if err = sample(); err != nil {
		return err
	}
	ticker := time.NewTicker(r.config.Interval)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Until(started.Add(r.config.Duration)))
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err = sample(); err != nil {
				return err
			}
		case <-deadline.C:
			ended := time.Now().UnixMilli()
			if err = logs.Freeze(); err != nil {
				return err
			}
			if err = profile.StopAndClose(); err != nil {
				return err
			}
			if err = metrics.syncAndClose(); err != nil {
				return err
			}
			if err = logs.CopyTo(dir, math.MaxInt64); err != nil {
				return err
			}
			return writeManifest(dir, id, r.config.Environment, r.config.Host, started, ended, r.rawArtifactNames()...)
		}
	}
}

func (r *Recorder) rawArtifactNames() []string {
	names := make([]string, 0, len(r.config.RawLogs))
	for _, label := range slices.Sorted(maps.Keys(r.config.RawLogs)) {
		names = append(names, rawArtifactName(label))
	}
	return names
}
