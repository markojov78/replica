package storage

import (
	"context"
	"time"
)

type SFTPWatcher struct {
	scanner  Scanner
	interval time.Duration
}

func (w *SFTPWatcher) Watch(ctx context.Context, uri string, targets []string) (<-chan FileChange, <-chan error, error) {
	interval := w.interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return watchSnapshots(ctx, w.scanner, interval, uri, targets)
}
