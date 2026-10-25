package fgprof

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/felixge/fgprof"
)

const ProfileName = "fgprof.pprof"

// Session owns the profile artifact and the profiler's stop lifecycle.
type Session struct {
	file    *os.File
	stop    func() error
	stopped bool
}

func Start(dir string) (*Session, error) {
	file, err := os.OpenFile(filepath.Join(dir, ProfileName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	return newSession(file, fgprof.Start(file, fgprof.FormatPprof)), nil
}

func newSession(file *os.File, stop func() error) *Session {
	return &Session{file: file, stop: stop}
}

func (s *Session) StopAndClose() error {
	if s.stopped {
		return nil
	}
	s.stopped = true
	return errors.Join(stopSafely(s.stop), s.file.Close())
}

// fgprof 0.9.5 can panic when stopped before its first sample (zero samples).
// Profiling failure must not crash the application during shutdown.
func stopSafely(stop func() error) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("fgprof stop failed: %v", value)
		}
	}()
	return stop()
}
