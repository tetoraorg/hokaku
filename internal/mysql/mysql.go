package mysql

import "github.com/tetoraorg/hokaku/internal/logs"

const ArtifactName = "slow.log"

// Target binds the configured MySQL slow log to its captured artifact.
// Shared file safety and bounded snapshotting live in internal/logs.
func Target(path string) logs.Target {
	return logs.Target{SourcePath: path, ArtifactName: ArtifactName}
}
