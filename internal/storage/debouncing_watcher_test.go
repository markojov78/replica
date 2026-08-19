package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

type channelWatcher struct {
	changes <-chan FileChange
	errors  <-chan error
}

func (w channelWatcher) Watch(context.Context, string, []string) (<-chan FileChange, <-chan error, error) {
	return w.changes, w.errors, nil
}

func TestDebouncingWatcherResetsDelayForSamePath(t *testing.T) {
	sourceChanges := make(chan FileChange)
	sourceErrors := make(chan error)
	defer close(sourceChanges)
	defer close(sourceErrors)

	delay := 80 * time.Millisecond
	resolvedState := FileState{RelativeURI: "photo.jpg", Size: 30, Hash: "final"}
	resolveCalls := 0
	watcher := NewDebouncingWatcher(channelWatcher{sourceChanges, sourceErrors}, delay,
		func(_ context.Context, _ string, change FileChange) (FileChange, bool, error) {
			resolveCalls++
			change.State = &resolvedState
			return change, true, nil
		})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	changes, _, err := watcher.Watch(ctx, "/replica", nil)
	if err != nil {
		t.Fatalf("Watch() error = %v", err)
	}

	sourceChanges <- FileChange{RelativeURI: "photo.jpg", ChangeType: FileChangeTypeCreated}
	time.Sleep(delay / 2)
	sourceChanges <- FileChange{RelativeURI: "photo.jpg", ChangeType: FileChangeTypeModified}

	select {
	case change := <-changes:
		t.Fatalf("change emitted before reset delay expired: %+v", change)
	case <-time.After(delay * 3 / 4):
	}

	select {
	case change := <-changes:
		if change.RelativeURI != "photo.jpg" || change.State == nil || change.State.Hash != "final" {
			t.Fatalf("change = %+v, want resolved final state", change)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for debounced change")
	}
	if resolveCalls != 1 {
		t.Fatalf("resolveCalls = %d, want 1", resolveCalls)
	}
}

func TestDebouncingWatcherTracksPathsIndependently(t *testing.T) {
	sourceChanges := make(chan FileChange)
	sourceErrors := make(chan error)
	defer close(sourceChanges)
	defer close(sourceErrors)

	delay := 60 * time.Millisecond
	watcher := NewDebouncingWatcher(channelWatcher{sourceChanges, sourceErrors}, delay, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	changes, _, err := watcher.Watch(ctx, "/replica", nil)
	if err != nil {
		t.Fatalf("Watch() error = %v", err)
	}

	sourceChanges <- FileChange{RelativeURI: "first.jpg", ChangeType: FileChangeTypeModified}
	time.Sleep(delay / 2)
	sourceChanges <- FileChange{RelativeURI: "second.jpg", ChangeType: FileChangeTypeModified}

	select {
	case change := <-changes:
		if change.RelativeURI != "first.jpg" {
			t.Fatalf("first change = %+v, want first.jpg", change)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for first path")
	}
	select {
	case change := <-changes:
		if change.RelativeURI != "second.jpg" {
			t.Fatalf("second change = %+v, want second.jpg", change)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for second path")
	}
}

func TestDebouncingWatcherForwardsResolverErrors(t *testing.T) {
	sourceChanges := make(chan FileChange)
	sourceErrors := make(chan error)
	defer close(sourceChanges)
	defer close(sourceErrors)

	wantErr := errors.New("resolve failed")
	watcher := NewDebouncingWatcher(channelWatcher{sourceChanges, sourceErrors}, time.Millisecond,
		func(context.Context, string, FileChange) (FileChange, bool, error) {
			return FileChange{}, false, wantErr
		})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, errCh, err := watcher.Watch(ctx, "/replica", nil)
	if err != nil {
		t.Fatalf("Watch() error = %v", err)
	}

	sourceChanges <- FileChange{RelativeURI: "photo.jpg", ChangeType: FileChangeTypeModified}
	select {
	case gotErr := <-errCh:
		if !errors.Is(gotErr, wantErr) {
			t.Fatalf("error = %v, want %v", gotErr, wantErr)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for resolver error")
	}
}

func TestDebouncingWatcherDropsPendingChangesOnCancellation(t *testing.T) {
	sourceChanges := make(chan FileChange)
	sourceErrors := make(chan error)
	watcher := NewDebouncingWatcher(channelWatcher{sourceChanges, sourceErrors}, time.Hour, nil)
	ctx, cancel := context.WithCancel(context.Background())
	changes, errors, err := watcher.Watch(ctx, "/replica", nil)
	if err != nil {
		t.Fatalf("Watch() error = %v", err)
	}

	sourceChanges <- FileChange{RelativeURI: "photo.jpg", ChangeType: FileChangeTypeModified}
	cancel()

	select {
	case _, ok := <-changes:
		if ok {
			t.Fatal("pending change was emitted after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("change channel did not close after cancellation")
	}
	select {
	case _, ok := <-errors:
		if ok {
			t.Fatal("error channel remained open after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("error channel did not close after cancellation")
	}
	close(sourceChanges)
	close(sourceErrors)
}
