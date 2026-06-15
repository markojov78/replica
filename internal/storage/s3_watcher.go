package storage

import (
	"context"
	"time"
)

const defaultS3PollInterval = 30 * time.Second

type S3Watcher struct {
	scanner  Scanner
	interval time.Duration
}

func NewS3Watcher(scanner Scanner, interval time.Duration) *S3Watcher {
	if interval <= 0 {
		interval = defaultS3PollInterval
	}
	return &S3Watcher{
		scanner:  scanner,
		interval: interval,
	}
}

func (w *S3Watcher) Watch(ctx context.Context, rootURI string, targetRelativeURIs []string) (<-chan FileChange, <-chan error, error) {
	initial, err := w.scanner.Scan(ctx, rootURI, nil, targetRelativeURIs...)
	if err != nil {
		return nil, nil, err
	}

	changeCh := make(chan FileChange)
	errCh := make(chan error, 1)

	go func() {
		defer close(changeCh)
		defer close(errCh)

		previous := initial
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, err := w.scanner.Scan(ctx, rootURI, fileStateMap(previous), targetRelativeURIs...)
				if err != nil {
					sendError(ctx, errCh, err)
					continue
				}

				for _, change := range compareSnapshots(previous, current) {
					sendChange(ctx, changeCh, change)
				}
				previous = current
			}
		}
	}()

	return changeCh, errCh, nil
}
