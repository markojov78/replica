//go:build linux && !arm

package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"replica/internal/apiclient"
	"replica/internal/config"
)

type testFuseServer struct {
	calls    int
	failures int
	entered  chan struct{}
	release  chan struct{}
}

func (s *testFuseServer) Unmount() error {
	s.calls++
	if s.entered != nil {
		close(s.entered)
		<-s.release
	}
	if s.calls <= s.failures {
		return errors.New("mount busy")
	}
	return nil
}

func TestFuseMountInfoOwnershipAndEscapes(t *testing.T) {
	input := fmt.Sprintf("20 1 0:1 / /tmp/my\\040shares/Album rw - fuse.Album replica-share ro,user_id=%d,group_id=1000\n", os.Geteuid())
	mounts, err := parseFuseMounts(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	info, ok := mounts["/tmp/my shares/Album"]
	if !ok || !info.ownedReplicaMount() {
		t.Fatalf("unexpected mounts: %#v", mounts)
	}
	for _, foreign := range []fuseMountInfo{
		{fsType: "fuse.Album", source: "other", options: info.options},
		{fsType: "ext4", source: "replica-share", options: info.options},
		{fsType: "fuse.Album", source: "replica-share", options: "user_id=999999"},
	} {
		if foreign.ownedReplicaMount() {
			t.Fatalf("accepted foreign mount: %#v", foreign)
		}
	}
	stacked, err := parseFuseMounts(strings.NewReader(input + input))
	if err != nil {
		t.Fatal(err)
	}
	if stacked["/tmp/my shares/Album"].ownedReplicaMount() {
		t.Fatal("accepted stacked mount")
	}
	if _, err := parseFuseMounts(strings.NewReader("invalid\n")); err == nil {
		t.Fatal("accepted invalid mountinfo")
	}
}

func TestFusePrefixLockExcludesOtherManagers(t *testing.T) {
	prefix := t.TempDir()
	a := &fuseManager{cfg: config.FuseConfig{PathPrefix: prefix}}
	b := &fuseManager{cfg: a.cfg}
	defer a.Stop(context.Background())
	defer b.Stop(context.Background())
	if err := a.preparePrefix(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.preparePrefix(context.Background()); err == nil {
		t.Fatal("second manager acquired prefix")
	}
	if err := a.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.preparePrefix(context.Background()); err != nil {
		t.Fatalf("lock not released: %v", err)
	}
}

func TestFusePrefixLockRejectsSymlink(t *testing.T) {
	prefix := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(prefix, ".replica-fuse.lock")); err != nil {
		t.Fatal(err)
	}
	m := &fuseManager{cfg: config.FuseConfig{PathPrefix: prefix}}
	if err := m.preparePrefix(context.Background()); err == nil {
		t.Fatal("accepted symlink lock")
	}
}

func TestFuseStopRetriesAndWaits(t *testing.T) {
	server := &testFuseServer{failures: 1}
	m := &fuseManager{mounts: map[uint]*fuseMount{1: {shareID: 1, server: server}}}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if server.calls != 2 || len(m.mounts) != 0 {
		t.Fatalf("calls=%d mounts=%d", server.calls, len(m.mounts))
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if server.calls != 2 {
		t.Fatal("stop was not idempotent")
	}
	// No runtime is needed: a stopped manager must return before consulting it.
	m.reconcile(context.Background())
}

func TestFuseStopRetainsFailedMount(t *testing.T) {
	server := &testFuseServer{failures: 10}
	m := &fuseManager{mounts: map[uint]*fuseMount{1: {shareID: 1, server: server}}}
	if err := m.Stop(context.Background()); err == nil {
		t.Fatal("expected cleanup failure")
	}
	if server.calls != 3 || len(m.mounts) != 1 {
		t.Fatalf("calls=%d mounts=%d", server.calls, len(m.mounts))
	}
}

func TestFuseStopDeadlineDoesNotPretendCleanupFinished(t *testing.T) {
	server := &testFuseServer{entered: make(chan struct{}), release: make(chan struct{})}
	m := &fuseManager{mounts: map[uint]*fuseMount{1: {shareID: 1, server: server}}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := m.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop: %v", err)
	}
	<-server.entered
	close(server.release)
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFuseReconcileRetainsFailedUnmountAndRetries(t *testing.T) {
	client, err := apiclient.New(config.Config{App: config.AppConfig{NodeID: "node", NodeAddress: "http://127.0.0.1", CoordinatorURL: "http://127.0.0.1"}, Auth: config.AuthConfig{NodeSecret: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	server := &testFuseServer{failures: 1}
	m := &fuseManager{runtime: &Runtime{client: client}, mounts: map[uint]*fuseMount{1: {shareID: 1, server: server}}}
	m.reconcile(context.Background())
	if len(m.mounts) != 1 {
		t.Fatal("failed mount was forgotten")
	}
	m.reconcile(context.Background())
	if len(m.mounts) != 0 || server.calls != 2 {
		t.Fatal("unmount was not retried")
	}
}

func TestFuseCancelledReconcileDoesNotInitialize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := &fuseManager{}
	m.reconcile(ctx)
}
