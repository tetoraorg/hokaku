package memory

import "testing"

func TestSnapshotKeepAvailableMeaning(t *testing.T) {
	got, err := newSnapshot(100, 37)
	if err != nil {
		t.Fatal(err)
	}
	if got != (Snapshot{Total: 100, Available: 37}) {
		t.Fatalf("newSnapshot() = %+v", got)
	}
}

func TestSnapshotRejectImpossibleValues(t *testing.T) {
	for _, test := range []struct {
		name      string
		total     uint64
		available uint64
	}{
		{name: "zero total", total: 0, available: 0},
		{name: "available exceeds total", total: 10, available: 11},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newSnapshot(test.total, test.available); err == nil {
				t.Fatal("accepted invalid memory counters")
			}
		})
	}
}
