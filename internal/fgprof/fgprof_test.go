package fgprof

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionStopsAndClosesProfile(t *testing.T) {
	dir := t.TempDir()
	session, err := Start(dir)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err = session.StopAndClose(); err != nil {
		t.Fatal(err)
	}
	if err = session.StopAndClose(); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, ProfileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("profile was not flushed")
	}
	if _, err = session.file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("profile file still open: %v", err)
	}
}

func TestStopFailureStillClosesProfile(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "profile-*.pprof")
	if err != nil {
		t.Fatal(err)
	}
	session := newSession(file, func() error { return errors.New("stop failed") })
	err = session.StopAndClose()
	if err == nil || !strings.Contains(err.Error(), "stop failed") {
		t.Fatalf("StopAndClose() error = %v", err)
	}
	if err = session.StopAndClose(); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if _, err = file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("profile file still open after stop error: %v", err)
	}
}

func TestStopPanicIsRecoveredAndClosesProfile(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "profile-*.pprof")
	if err != nil {
		t.Fatal(err)
	}
	session := newSession(file, func() error { panic("zero samples") })
	err = session.StopAndClose()
	if err == nil || !strings.Contains(err.Error(), "fgprof stop failed: zero samples") {
		t.Fatalf("StopAndClose() error = %v", err)
	}
	if _, err = file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("profile file still open after panic: %v", err)
	}
}
