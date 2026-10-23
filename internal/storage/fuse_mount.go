//go:build linux && !arm

package storage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"replica/internal/apiclient"
	"replica/internal/config"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// fuseManager owns the read-only FUSE mounts exposed by a storage node. Each
// active share assigned to this node with the fuse_enabled property is mounted
// at <path_prefix>/<share-name> while sharing is enabled on the node. Mounts are
// created, updated and removed from the same state that the sharing API uses,
// driven by startup hydration, refresh_state and refresh_config commands.
type fuseManager struct {
	runtime *Runtime
	cfg     config.FuseConfig

	mu         sync.Mutex
	mounts     map[uint]*fuseMount
	stopped    bool
	prefixLock *os.File
	stopOnce   sync.Once
	stopDone   chan struct{}
	stopErr    error
}

type fuseMount struct {
	shareID    uint
	mountPoint string
	server     interface{ Unmount() error }
}

type desiredMount struct {
	share      apiclient.Share
	replica    apiclient.Replica
	mountPoint string
}

func newStorageFuseManager(cfg config.Config) *fuseManager {
	if !cfg.App.Storage {
		return nil
	}
	return &fuseManager{cfg: cfg.Sharing.Fuse, mounts: make(map[uint]*fuseMount)}
}

func newFuseManager(r *Runtime) *fuseManager {
	return &fuseManager{runtime: r, cfg: r.cfg.Sharing.Fuse, mounts: make(map[uint]*fuseMount)}
}

func (m *fuseManager) Start(ctx context.Context) {
	m.mu.Lock()
	if !m.stopped && ctx.Err() == nil && m.cfg.PathPrefix != "" {
		if err := m.preparePrefix(ctx); err != nil {
			log.Printf("storage runtime fuse prepare failed: %v", err)
		}
	}
	m.mu.Unlock()
	m.reconcile(ctx)
	go func() {
		<-ctx.Done()
		_ = m.Stop(context.Background())
	}()
}

func (m *fuseManager) reconcile(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped || ctx.Err() != nil {
		return
	}

	replicas := make(map[uint]apiclient.Replica)
	for _, replica := range m.runtime.replicasSnapshot() {
		replicas[replica.ID] = replica
	}
	nodeID := m.runtime.client.NodeID()

	desired := make(map[uint]desiredMount)
	usedMountPoints := make(map[string]uint)
	sharingEnabled := m.runtime.SharingEnabled()
	for _, share := range m.runtime.sharesSnapshot() {
		if !sharingEnabled || !shareFuseEnabled(share) {
			continue
		}
		replica, ok := replicas[share.ReplicaID]
		if !ok {
			continue
		}
		if !shareAvailableOnReplica(share, replica, nodeID) {
			continue
		}
		if m.cfg.PathPrefix == "" {
			log.Printf("storage runtime fuse mount skipped share_id=%d: sharing.fuse.path_prefix is not configured", share.ID)
			continue
		}
		mountPoint := m.mountPoint(share, usedMountPoints)
		usedMountPoints[mountPoint] = share.ID
		desired[share.ID] = desiredMount{share: share, replica: replica, mountPoint: mountPoint}
	}

	if len(desired) > 0 {
		if err := m.preparePrefix(ctx); err != nil {
			log.Printf("storage runtime fuse prepare failed: %v", err)
			return
		}
		// The prefix may only have become resolvable after creating it.
		for id, want := range desired {
			want.mountPoint = filepath.Join(filepath.Dir(m.prefixLock.Name()), filepath.Base(want.mountPoint))
			desired[id] = want
		}
	}

	for shareID, existing := range m.mounts {
		want, ok := desired[shareID]
		if !ok || want.mountPoint != existing.mountPoint {
			if err := m.unmount(existing); err == nil {
				delete(m.mounts, shareID)
			}
		}
	}

	for shareID, want := range desired {
		if _, ok := m.mounts[shareID]; ok {
			continue
		}
		occupied := false
		for _, existing := range m.mounts {
			if existing.mountPoint == want.mountPoint {
				occupied = true
				break
			}
		}
		if !occupied && ctx.Err() == nil {
			m.mount(ctx, want)
		}
	}
}

func shareFuseEnabled(share apiclient.Share) bool {
	return share.Properties.FuseEnabled != nil && *share.Properties.FuseEnabled
}

func (m *fuseManager) mountPoint(share apiclient.Share, used map[string]uint) string {
	base := sanitizeShareName(share.Name)
	if base == "" {
		base = fmt.Sprintf("share-%d", share.ID)
	}
	prefix := m.cfg.PathPrefix
	if resolved, err := filepath.EvalSymlinks(prefix); err == nil {
		prefix = resolved
	}
	candidate := filepath.Join(prefix, base)
	if other, ok := used[candidate]; ok && other != share.ID {
		candidate = filepath.Join(prefix, base+"-"+strconv.FormatUint(uint64(share.ID), 10))
	}
	return candidate
}

func (m *fuseManager) mount(ctx context.Context, want desiredMount) {
	if err := m.recoverMountPoint(ctx, want.mountPoint); err != nil {
		log.Printf("storage runtime fuse mount point unavailable share_id=%d path=%s: %v", want.share.ID, want.mountPoint, err)
		return
	}
	m.mountAttempt(ctx, want, true)
}

func (m *fuseManager) mountAttempt(ctx context.Context, want desiredMount, retry bool) {
	if ctx.Err() != nil {
		return
	}
	if info, err := os.Lstat(want.mountPoint); err == nil && info.Mode()&os.ModeSymlink != 0 {
		log.Printf("storage runtime fuse mount refused symlink path=%s", want.mountPoint)
		return
	}
	if err := os.MkdirAll(want.mountPoint, 0o755); err != nil {
		log.Printf("storage runtime fuse mkdir failed share_id=%d path=%s: %v", want.share.ID, want.mountPoint, err)
		m.retryMount(ctx, want, retry)
		return
	}

	profile, err := m.runtime.GetPprofile(want.replica.StorageProfile)
	if err != nil {
		log.Printf("storage runtime fuse profile lookup failed share_id=%d replica_id=%d: %v", want.share.ID, want.replica.ID, err)
		return
	}

	shareFS := newShareFS(m.runtime, want.share.ID, want.replica.ID, want.replica.URI, profile)
	root := &shareDirNode{prefix: "", s: shareFS}

	entryTimeout := m.cfg.EntryTimeout
	attrTimeout := m.cfg.AttrTimeout
	options := &fs.Options{
		MountOptions: fuse.MountOptions{
			AllowOther: m.cfg.AllowOther,
			Options:    []string{"ro"},
			FsName:     "replica-share",
			Name:       filepath.Base(want.mountPoint),
		},
		EntryTimeout: &entryTimeout,
		AttrTimeout:  &attrTimeout,
	}

	server, err := fs.Mount(want.mountPoint, root, options)
	if err != nil {
		log.Printf("storage runtime fuse mount failed share_id=%d name=%q path=%s: %v", want.share.ID, want.share.Name, want.mountPoint, err)
		m.retryMount(ctx, want, retry)
		return
	}

	m.mounts[want.share.ID] = &fuseMount{shareID: want.share.ID, mountPoint: want.mountPoint, server: server}
	log.Printf("storage runtime fuse mounted share_id=%d name=%q path=%s", want.share.ID, want.share.Name, want.mountPoint)
}

func (m *fuseManager) retryMount(ctx context.Context, want desiredMount, retry bool) {
	if !retry || ctx.Err() != nil {
		return
	}
	mounts, err := readFuseMounts()
	if err != nil {
		return
	}
	if _, ok := mounts[want.mountPoint]; !ok {
		return
	}
	if err := m.recoverMountPoint(ctx, want.mountPoint); err != nil {
		log.Printf("storage runtime fuse recovery failed path=%s: %v", want.mountPoint, err)
		return
	}
	m.mountAttempt(ctx, want, false)
}

func (m *fuseManager) unmount(existing *fuseMount) error {
	if existing.server != nil {
		if err := existing.server.Unmount(); err != nil {
			log.Printf("storage runtime fuse unmount failed share_id=%d path=%s: %v", existing.shareID, existing.mountPoint, err)
			return err
		}
	}
	log.Printf("storage runtime fuse unmounted share_id=%d path=%s", existing.shareID, existing.mountPoint)
	return nil
}

func (m *fuseManager) Stop(ctx context.Context) error {
	m.stopOnce.Do(func() {
		m.stopDone = make(chan struct{})
		go func() {
			defer close(m.stopDone)
			m.mu.Lock()
			defer m.mu.Unlock()
			m.stopped = true
			for attempt := 0; attempt < 3; attempt++ {
				var errs []error
				for shareID, existing := range m.mounts {
					if err := m.unmount(existing); err != nil {
						errs = append(errs, fmt.Errorf("unmount %s: %w", existing.mountPoint, err))
					} else {
						delete(m.mounts, shareID)
					}
				}
				m.stopErr = errors.Join(errs...)
				if m.stopErr == nil {
					break
				}
				if attempt < 2 {
					time.Sleep(100 * time.Millisecond)
				}
			}
			if m.prefixLock != nil {
				m.stopErr = errors.Join(m.stopErr, m.prefixLock.Close())
				m.prefixLock = nil
			}
		}()
	})
	select {
	case <-m.stopDone:
		return m.stopErr
	case <-ctx.Done():
		return fmt.Errorf("FUSE shutdown: %w", ctx.Err())
	}
}

func sanitizeShareName(name string) string {
	var builder strings.Builder
	prevDash := false
	for _, r := range name {
		keep := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.'
		if keep {
			builder.WriteRune(r)
			prevDash = r == '-'
			continue
		}
		if !prevDash {
			builder.WriteRune('-')
			prevDash = true
		}
	}
	result := strings.Trim(builder.String(), "-.")
	if result == "." || result == ".." {
		return ""
	}
	return result
}
