package storage

import (
	"context"
	"time"
)

// FileChangeResolver inspects a debounced change and returns the current state
// that should be emitted. A false result suppresses the change.
type FileChangeResolver func(context.Context, string, FileChange) (FileChange, bool, error)

// DebouncingWatcher coalesces changes to the same path until no new change has
// arrived for the configured delay.
type DebouncingWatcher struct {
	watcher  Watcher
	delay    time.Duration
	resolver FileChangeResolver
}

func NewDebouncingWatcher(watcher Watcher, delay time.Duration, resolver FileChangeResolver) *DebouncingWatcher {
	return &DebouncingWatcher{watcher: watcher, delay: delay, resolver: resolver}
}

func (w *DebouncingWatcher) Watch(ctx context.Context, rootURI string, targetRelativeURIs []string) (<-chan FileChange, <-chan error, error) {
	changes, errors, err := w.watcher.Watch(ctx, rootURI, targetRelativeURIs)
	if err != nil {
		return nil, nil, err
	}

	changeCh := make(chan FileChange)
	errCh := make(chan error, 1)
	go w.run(ctx, rootURI, changes, errors, changeCh, errCh)
	return changeCh, errCh, nil
}

type pendingDebouncedChange struct {
	change   FileChange
	deadline time.Time
}

func (w *DebouncingWatcher) run(
	ctx context.Context,
	rootURI string,
	changes <-chan FileChange,
	errors <-chan error,
	changeCh chan<- FileChange,
	errCh chan<- error,
) {
	defer close(changeCh)
	defer close(errCh)

	pending := make(map[string]pendingDebouncedChange)
	var timer *time.Timer
	var timerCh <-chan time.Time

	resetTimer := func() {
		if timer != nil {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		if len(pending) == 0 {
			timerCh = nil
			return
		}
		var next time.Time
		for _, item := range pending {
			if next.IsZero() || item.deadline.Before(next) {
				next = item.deadline
			}
		}
		delay := time.Until(next)
		if delay < 0 {
			delay = 0
		}
		if timer == nil {
			timer = time.NewTimer(delay)
		} else {
			timer.Reset(delay)
		}
		timerCh = timer.C
	}

	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for changes != nil || errors != nil || len(pending) > 0 {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-errors:
			if !ok {
				errors = nil
				continue
			}
			sendError(ctx, errCh, err)
		case change, ok := <-changes:
			if !ok {
				changes = nil
				continue
			}
			if change.RelativeURI == "" || change.ChangeType == FileChangeTypeRescanRequired {
				sendChange(ctx, changeCh, change)
				continue
			}
			pending[change.RelativeURI] = pendingDebouncedChange{
				change:   change,
				deadline: time.Now().Add(w.delay),
			}
			resetTimer()
		case now := <-timerCh:
			for relativeURI, item := range pending {
				if item.deadline.After(now) {
					continue
				}
				delete(pending, relativeURI)
				change := item.change
				if w.resolver != nil {
					resolved, ok, err := w.resolver(ctx, rootURI, change)
					if err != nil {
						sendError(ctx, errCh, err)
						continue
					}
					if !ok {
						continue
					}
					change = resolved
				}
				sendChange(ctx, changeCh, change)
			}
			resetTimer()
		}
	}
}
