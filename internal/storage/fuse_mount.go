package storage

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

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

	mu     sync.Mutex
	mounts map[uint]*fuseMount
}

type fuseMount struct {
	shareID    uint
	mountPoint string
	server     *fuse.Server
}

type desiredMount struct {
	share      apiclient.Share
	replica    apiclient.Replica
	mountPoint string
}

func newFuseManager(r *Runtime) *fuseManager {
	return &fuseManager{runtime: r, cfg: r.cfg.Sharing.Fuse, mounts: make(map[uint]*fuseMount)}
}

func (m *fuseManager) Start(ctx context.Context) {
	m.reconcile(ctx)
	go func() {
		<-ctx.Done()
		m.unmountAll()
	}()
}

func (m *fuseManager) reconcile(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()

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

	for shareID, existing := range m.mounts {
		want, ok := desired[shareID]
		if !ok || want.mountPoint != existing.mountPoint {
			m.unmount(existing)
			delete(m.mounts, shareID)
		}
	}

	for shareID, want := range desired {
		if _, ok := m.mounts[shareID]; ok {
			continue
		}
		m.mount(ctx, want)
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
	candidate := filepath.Join(m.cfg.PathPrefix, base)
	if other, ok := used[candidate]; ok && other != share.ID {
		candidate = filepath.Join(m.cfg.PathPrefix, base+"-"+strconv.FormatUint(uint64(share.ID), 10))
	}
	return candidate
}

func (m *fuseManager) mount(ctx context.Context, want desiredMount) {
	if err := os.MkdirAll(want.mountPoint, 0o755); err != nil {
		log.Printf("storage runtime fuse mkdir failed share_id=%d path=%s: %v", want.share.ID, want.mountPoint, err)
		return
	}

	profile, err := m.runtime.GetPprofile(want.replica.StorageProfile)
	if err != nil {
		log.Printf("storage runtime fuse profile lookup failed share_id=%d replica_id=%d: %v", want.share.ID, want.replica.ID, err)
		return
	}

	shareFS := newShareFS(m.runtime, want.replica.ID, want.replica.URI, profile)
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
		return
	}

	m.mounts[want.share.ID] = &fuseMount{shareID: want.share.ID, mountPoint: want.mountPoint, server: server}
	log.Printf("storage runtime fuse mounted share_id=%d name=%q path=%s", want.share.ID, want.share.Name, want.mountPoint)
}

func (m *fuseManager) unmount(existing *fuseMount) {
	if existing.server != nil {
		if err := existing.server.Unmount(); err != nil {
			log.Printf("storage runtime fuse unmount failed share_id=%d path=%s: %v", existing.shareID, existing.mountPoint, err)
			return
		}
	}
	log.Printf("storage runtime fuse unmounted share_id=%d path=%s", existing.shareID, existing.mountPoint)
}

func (m *fuseManager) unmountAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for shareID, existing := range m.mounts {
		m.unmount(existing)
		delete(m.mounts, shareID)
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
