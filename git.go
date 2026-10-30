package hokaku

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type gitMetadata struct {
	Commit  string `json:"commit"`
	Message string `json:"message"`
	Dirty   bool   `json:"dirty"`
}

var gitCommitHash = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

func readGitMetadata(dir string) (*gitMetadata, error) {
	if dir == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
		output, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("collect Git metadata (%s): %w", args[0], err)
		}
		return string(output), nil
	}
	log, err := run("log", "-1", "--format=%H%x00%s", "HEAD")
	if err != nil {
		return nil, err
	}
	commit, message, ok := strings.Cut(strings.TrimSuffix(log, "\n"), "\x00")
	if !ok || !gitCommitHash.MatchString(commit) {
		return nil, fmt.Errorf("invalid Git HEAD metadata")
	}
	status, err := run("status", "--porcelain=v1", "--untracked-files=normal", "--ignore-submodules=none")
	if err != nil {
		return nil, err
	}
	dirty := status != ""
	if dirty {
		message = "[+]" + message
	}
	if len(message) > 4096 {
		return nil, fmt.Errorf("Git commit message exceeds 4096 bytes")
	}
	if dirty {
		slog.Warn("hokaku: Git checkout has uncommitted changes", "git_dir", dir, "commit", commit)
	}
	return &gitMetadata{Commit: commit, Message: message, Dirty: dirty}, nil
}
