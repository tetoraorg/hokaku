package hokaku

import (
	"context"
	"fmt"
	"os/exec"
)

// CommandRotator invokes a fixed operator-provided command without a shell.
// Configure a narrowly scoped sudo rule when privileged log reopening is needed.
func CommandRotator(command string, args ...string) func(context.Context) error {
	return func(ctx context.Context) error {
		cmd := exec.CommandContext(ctx, command, args...)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("log rotation command failed: %w", err)
		}
		return nil
	}
}
