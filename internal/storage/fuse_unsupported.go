//go:build !linux || arm

package storage

import (
	"context"
	"log"
	"runtime"

	"replica/internal/config"
)

// fuseManager is a no-op on platforms without FUSE support. Shares with the
// fuse_enabled property are still served through the sharing API.
type fuseManager struct {
	runtime *Runtime
}

func newStorageFuseManager(cfg config.Config) *fuseManager {
	if cfg.App.Storage && cfg.Sharing.Fuse.PathPrefix != "" {
		log.Printf("storage runtime fuse disabled: FUSE shares are not supported on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return nil
}

func (m *fuseManager) Start(_ context.Context) {}

func (m *fuseManager) reconcile(_ context.Context) {}
