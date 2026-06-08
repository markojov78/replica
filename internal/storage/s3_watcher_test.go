package storage

import (
	"context"
	"testing"
	"time"
)

func TestCompareSnapshotsDetectsCreatedModifiedAndDeleted(t *testing.T) {
	modifiedA := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	modifiedB := modifiedA.Add(time.Minute)

	previous := []FileState{
		{RelativeURI: "deleted.txt", Size: 1, Modified: modifiedA, Hash: "a", HashAlgorithm: HashAlgorithmBLAKE3},
		{RelativeURI: "modified.txt", Size: 1, Modified: modifiedA, Hash: "a", HashAlgorithm: HashAlgorithmBLAKE3},
	}
	current := []FileState{
		{RelativeURI: "created.txt", Size: 2, Modified: modifiedA, Hash: "b", HashAlgorithm: HashAlgorithmBLAKE3},
		{RelativeURI: "modified.txt", Size: 2, Modified: modifiedB, Hash: "c", HashAlgorithm: HashAlgorithmBLAKE3},
	}

	changes := compareSnapshots(previous, current)
	if len(changes) != 3 {
		t.Fatalf("len(changes) = %d, want 3", len(changes))
	}
	if changes[0].RelativeURI != "created.txt" || changes[0].ChangeType != FileChangeTypeCreated {
		t.Fatalf("changes[0] = %+v, want created for created.txt", changes[0])
	}
	if changes[1].RelativeURI != "deleted.txt" || changes[1].ChangeType != FileChangeTypeDeleted {
		t.Fatalf("changes[1] = %+v, want deleted for deleted.txt", changes[1])
	}
	if changes[2].RelativeURI != "modified.txt" || changes[2].ChangeType != FileChangeTypeModified {
		t.Fatalf("changes[2] = %+v, want modified for modified.txt", changes[2])
	}
}

func TestS3WatcherEmitsSnapshotDiffs(t *testing.T) {
	scanner := &sequenceScanner{
		results: [][]FileState{
			{{RelativeURI: "file.txt", Size: 1, Modified: time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC), Hash: "a", HashAlgorithm: HashAlgorithmBLAKE3}},
			{{RelativeURI: "file.txt", Size: 2, Modified: time.Date(2026, 5, 2, 10, 1, 0, 0, time.UTC), Hash: "b", HashAlgorithm: HashAlgorithmBLAKE3}},
		},
	}
	watcher := NewS3Watcher(scanner, 10*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	changeCh, errCh, err := watcher.Watch(ctx, "s3://bucket")
	if err != nil {
		t.Fatalf("Watch() error = %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("unexpected watcher error: %v", err)
		}
	case change := <-changeCh:
		if change.RelativeURI != "file.txt" || change.ChangeType != FileChangeTypeModified {
			t.Fatalf("change = %+v, want modified file.txt", change)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for watcher change")
	}
}

type sequenceScanner struct {
	results [][]FileState
	index   int
}

func (s *sequenceScanner) Scan(_ context.Context, _ string, _ map[string]FileState) ([]FileState, error) {
	if s.index >= len(s.results) {
		return s.results[len(s.results)-1], nil
	}
	result := s.results[s.index]
	s.index++
	return result, nil
}
