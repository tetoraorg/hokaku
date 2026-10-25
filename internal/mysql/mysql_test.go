package mysql

import (
	"testing"

	"github.com/tetoraorg/hokaku/internal/logs"
)

func TestTargetUsesSlowLogArtifact(t *testing.T) {
	const path = "/var/log/mysql/slow.log"
	if got, want := Target(path), (logs.Target{SourcePath: path, ArtifactName: ArtifactName}); got != want {
		t.Fatalf("Target() = %+v, want %+v", got, want)
	}
	if ArtifactName != "slow.log" {
		t.Fatalf("ArtifactName = %q", ArtifactName)
	}
}
