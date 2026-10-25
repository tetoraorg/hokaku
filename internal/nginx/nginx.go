package nginx

import "github.com/tetoraorg/hokaku/internal/logs"

const ArtifactName = "access.log"

// Target binds the configured NGINX access log to its captured artifact.
// Shared file safety and bounded snapshotting live in internal/logs.
func Target(path string) logs.Target {
	return logs.Target{SourcePath: path, ArtifactName: ArtifactName}
}
