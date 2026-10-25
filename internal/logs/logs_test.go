package logs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotExcludesWritesAfterDeadline(t *testing.T) {
	for _, target := range testTargets() {
		t.Run(target.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "source.log")
			before := []byte("during benchmark\n")
			if err := os.WriteFile(path, before, 0600); err != nil {
				t.Fatal(err)
			}
			snapshot, err := Open(Target{SourcePath: path, ArtifactName: target.artifact})
			if err != nil {
				t.Fatal(err)
			}
			defer snapshot.Close()
			if err = snapshot.Freeze(); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.WriteString("after deadline\n"); err != nil {
				t.Fatal(err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, "out")
			if err = os.Mkdir(out, 0700); err != nil {
				t.Fatal(err)
			}
			if err = snapshot.CopyTo(out, 1024); err != nil {
				t.Fatal(err)
			}
			actual, err := os.ReadFile(filepath.Join(out, target.artifact))
			if err != nil {
				t.Fatal(err)
			}
			if string(actual) != string(before) {
				t.Fatalf("unexpected snapshot: %q", actual)
			}
		})
	}
}

func TestRotationAndTruncationDetected(t *testing.T) {
	for _, target := range testTargets() {
		for _, mode := range []string{"rename", "truncate", "rewrite"} {
			t.Run(target.name+"/"+mode, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "source.log")
				if err := os.WriteFile(path, []byte("original\n"), 0600); err != nil {
					t.Fatal(err)
				}
				snapshot, err := Open(Target{SourcePath: path, ArtifactName: target.artifact})
				if err != nil {
					t.Fatal(err)
				}
				defer snapshot.Close()
				switch mode {
				case "rename":
					err = os.Rename(path, path+".old")
					if err == nil {
						err = os.WriteFile(path, []byte("new\n"), 0600)
					}
				case "truncate":
					err = os.Truncate(path, 0)
				case "rewrite":
					err = os.WriteFile(path, []byte("replaced\n"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = snapshot.Check(); err == nil {
					t.Fatal("missed log change")
				}
			})
		}
	}
}

type testTarget struct {
	name     string
	artifact string
}

func testTargets() []testTarget {
	return []testTarget{
		{name: "nginx", artifact: "access.log"},
		{name: "mysql", artifact: "slow.log"},
	}
}

func TestSnapshotRejectsAboveConfiguredLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.log")
	if err := os.WriteFile(path, []byte("12345\n"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Open(Target{SourcePath: path, ArtifactName: "access.log"})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if err := snapshot.Freeze(); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.CopyTo(dir, 5); err == nil {
		t.Fatal("accepted oversized log")
	}
}
