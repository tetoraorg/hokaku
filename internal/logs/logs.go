package logs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type Target struct {
	SourcePath   string
	ArtifactName string
	Raw          bool // Capture append-only bytes from open time, without line validation.
}

type fileSnapshot struct {
	target    Target
	file      *os.File
	info      os.FileInfo
	size      int64
	endSize   int64
	startSize int64
	anchor    []byte
}

// Snapshot owns pinned log descriptors and the bytes captured at Freeze.
type Snapshot struct {
	files []*fileSnapshot
}

func Open(targets ...Target) (*Snapshot, error) {
	snapshot := &Snapshot{files: make([]*fileSnapshot, 0, len(targets))}
	for _, target := range targets {
		file := &fileSnapshot{target: target}
		snapshot.files = append(snapshot.files, file)
		if target.SourcePath == "" {
			continue
		}
		f, err := os.Open(target.SourcePath)
		if err != nil {
			snapshot.Close()
			return nil, err
		}
		file.file = f
		file.info, err = f.Stat()
		if err != nil || !file.info.Mode().IsRegular() {
			snapshot.Close()
			return nil, errors.New("log is not a regular file")
		}
		if err = file.check(); err != nil {
			snapshot.Close()
			return nil, err
		}
		if target.Raw {
			file.startSize = file.size
		}
	}
	return snapshot, nil
}

func (s *Snapshot) Check() error {
	for _, file := range s.files {
		if err := file.check(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Snapshot) Close() {
	for _, file := range s.files {
		if file.file != nil {
			_ = file.file.Close()
		}
	}
}

func (s *Snapshot) Freeze() error {
	if err := s.Check(); err != nil {
		return err
	}
	for _, file := range s.files {
		file.endSize = file.size
	}
	return nil
}

func (s *Snapshot) CopyTo(dir string, maxFileBytes int64) error {
	if err := s.Check(); err != nil {
		return err
	}
	for _, file := range s.files {
		f, err := os.OpenFile(filepath.Join(dir, file.target.ArtifactName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if file.file != nil {
			if file.target.Raw {
				size := file.endSize - file.startSize
				if size < 0 || size > maxFileBytes {
					err = fmt.Errorf("log exceeds %d bytes", maxFileBytes)
				} else {
					_, err = io.CopyN(f, io.NewSectionReader(file.file, file.startSize, size), size)
				}
			} else {
				err = copyBounded(f, file.file, file.endSize, maxFileBytes)
			}
		}
		if err == nil {
			err = f.Sync()
		}
		err = errors.Join(err, f.Close())
		if err != nil {
			return err
		}
		if !file.target.Raw && file.file != nil && file.endSize > 0 {
			var last [1]byte
			if _, err = file.file.ReadAt(last[:], file.endSize-1); err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			if last[0] != '\n' {
				return fmt.Errorf("incomplete final line: %s", file.target.ArtifactName)
			}
		}
	}
	return s.Check()
}

func (file *fileSnapshot) check() error {
	if file.file == nil {
		return nil
	}
	current, err := os.Stat(file.target.SourcePath)
	if err != nil {
		return err
	}
	if !os.SameFile(file.info, current) || current.Size() < file.size {
		return fmt.Errorf("log rotated or truncated during capture: %s", file.target.ArtifactName)
	}
	if len(file.anchor) > 0 {
		data := make([]byte, len(file.anchor))
		if _, err = file.file.ReadAt(data, file.size-int64(len(data))); err != nil {
			return err
		}
		if !bytes.Equal(data, file.anchor) {
			return fmt.Errorf("log rewritten during capture: %s", file.target.ArtifactName)
		}
	}
	file.size = current.Size()
	file.anchor = make([]byte, min(file.size, 64))
	if len(file.anchor) > 0 {
		_, err = file.file.ReadAt(file.anchor, file.size-int64(len(file.anchor)))
	}
	return err
}

func copyBounded(dst *os.File, src *os.File, size, maxFileBytes int64) error {
	if size < 0 || size > maxFileBytes {
		return fmt.Errorf("log exceeds %d bytes", maxFileBytes)
	}
	_, err := io.CopyN(dst, io.NewSectionReader(src, 0, size), size)
	return err
}
