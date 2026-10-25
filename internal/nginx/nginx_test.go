package nginx

import (
	"testing"

	"github.com/tetoraorg/hokaku/internal/logs"
)

func TestTargetUsesAccessLogArtifact(t *testing.T) {
	const path = "/var/log/nginx/access.jsonl"
	if got, want := Target(path), (logs.Target{SourcePath: path, ArtifactName: ArtifactName}); got != want {
		t.Fatalf("Target() = %+v, want %+v", got, want)
	}
	if ArtifactName != "access.log" {
		t.Fatalf("ArtifactName = %q", ArtifactName)
	}
}
