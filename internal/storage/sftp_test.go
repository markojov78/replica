package storage

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/zeebo/blake3"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"replica/internal/apiclient"
	"replica/internal/config"
)

func testSFTPServer(t *testing.T) (string, *config.StorageProfileConfig, string) {
	t.Helper()
	root := t.TempDir()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "identity")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), 0600); err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() != "replica" || !bytes.Equal(key.Marshal(), signer.PublicKey().Marshal()) {
			return nil, errors.New("wrong identity")
		}
		return nil, nil
	}}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hosts := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(hosts, []byte(knownhosts.Line([]string{listener.Addr().String()}, signer.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, raw)
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer raw.Close()
				conn, chans, reqs, err := ssh.NewServerConn(raw, serverConfig)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					if ch.ChannelType() != "session" {
						_ = ch.Reject(ssh.UnknownChannelType, "session required")
						continue
					}
					channel, requests, err := ch.Accept()
					if err != nil {
						return
					}
					go func() {
						for req := range requests {
							var payload struct{ Name string }
							_ = ssh.Unmarshal(req.Payload, &payload)
							_ = req.Reply(req.Type == "subsystem" && payload.Name == "sftp", nil)
						}
					}()
					server, err := sftp.NewServer(channel)
					if err != nil {
						channel.Close()
						return
					}
					_ = server.Serve()
					_ = server.Close()
				}
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-accepted
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	uri := (&url.URL{Scheme: "sftp", Host: listener.Addr().String(), Path: root}).String()
	return uri, &config.StorageProfileConfig{Type: "sftp", ProfileName: "test", Username: "replica", PrivateKeyFile: keyFile, KnownHostsFile: hosts}, root
}

func sftpTestHash(content string) string {
	sum := blake3.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func TestSFTPStorageRoundTrip(t *testing.T) {
	uri, profile, root := testSFTPServer(t)
	ctx := context.Background()
	writer, err := GetWriter(ctx, uri, profile)
	if err != nil {
		t.Fatal(err)
	}
	verified, ok := writer.(verifiedWriter)
	if !ok {
		t.Fatal("writer must verify replication content")
	}
	if err := verified.SaveVerified(ctx, uri, "nested/file.txt", strings.NewReader("content"), 7, sftpTestHash("content")); err != nil {
		t.Fatal(err)
	}
	reader, err := GetReader(ctx, uri, profile)
	if err != nil {
		t.Fatal(err)
	}
	r, size, err := reader.Open(ctx, uri, "nested/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	closeErr := r.Close()
	if err != nil || closeErr != nil || size != 7 || string(data) != "content" {
		t.Fatalf("read: size=%d body=%q err=%v close=%v", size, data, err, closeErr)
	}
	scanner, err := GetScanner(ctx, uri, profile)
	if err != nil {
		t.Fatal(err)
	}
	states, err := scanner.Scan(ctx, uri, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].RelativeURI != "nested/file.txt" || states[0].Hash != sftpTestHash("content") {
		t.Fatalf("states: %+v", states)
	}
	old := fileStateMap(states)
	cached := old["nested/file.txt"]
	cached.Hash = "cached-hash"
	old[cached.RelativeURI] = cached
	states, err = scanner.Scan(ctx, uri, old, "nested/file.txt", "missing.txt")
	if err != nil || len(states) != 1 || states[0].Hash != "cached-hash" {
		t.Fatalf("metadata reuse: %+v %v", states, err)
	}
	for _, tc := range []struct {
		size int64
		hash string
	}{{7, sftpTestHash("CONTENT")}, {6, sftpTestHash("content")}, {8, sftpTestHash("content")}} {
		err = verified.SaveVerified(ctx, uri, "nested/file.txt", strings.NewReader("content"), tc.size, tc.hash)
		if !errors.Is(err, ErrFileIntegrityMismatch) {
			t.Fatalf("mismatch: %v", err)
		}
		data, _ := os.ReadFile(filepath.Join(root, "nested/file.txt"))
		if string(data) != "content" {
			t.Fatal("mismatch replaced destination")
		}
		entries, _ := os.ReadDir(filepath.Join(root, "nested"))
		if len(entries) != 1 {
			t.Fatalf("temporary file leaked: %v", entries)
		}
	}
	if err := writer.Save(ctx, uri, "empty", strings.NewReader(""), 0); err != nil {
		t.Fatal(err)
	}
	if err := writer.Delete(ctx, uri, "nested/file.txt"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Delete(ctx, uri, "nested/file.txt"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reader.Open(ctx, uri, "nested/file.txt"); !errors.Is(err, errTransferFileNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err := writer.Save(ctx, uri, "../escape", strings.NewReader("x"), 1); err == nil {
		t.Fatal("accepted traversal")
	}
	if err := os.WriteFile(filepath.Join(root, TemporaryWritePrefix+"ignored"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	states, err = scanner.Scan(ctx, uri, nil)
	if err != nil || len(states) != 1 || states[0].RelativeURI != "empty" {
		t.Fatalf("temporary filter: %+v %v", states, err)
	}
	if err := os.Rename(root, root+"-gone"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(root+"-gone", root)
	if states, err = scanner.Scan(ctx, uri, nil); err == nil || states != nil {
		t.Fatalf("missing root must fail: %+v %v", states, err)
	}
}

func TestSFTPSymlinks(t *testing.T) {
	uri, profile, root := testSFTPServer(t)
	ctx := context.Background()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, dest := range map[string]string{"link": target, "broken": target + "missing", "dirlink": filepath.Dir(target)} {
		if err := os.Symlink(dest, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	ignored, _ := GetScanner(ctx, uri, profile, false)
	states, err := ignored.Scan(ctx, uri, nil)
	if err != nil || len(states) != 0 {
		t.Fatalf("ignored symlinks: %+v %v", states, err)
	}
	followed, _ := GetScanner(ctx, uri, profile, true)
	states, err = followed.Scan(ctx, uri, nil)
	if err != nil || len(states) != 1 || states[0].RelativeURI != "link" || states[0].Hash != sftpTestHash("old") {
		t.Fatalf("followed: %+v %v", states, err)
	}
	writer, _ := GetWriter(ctx, uri, profile, true)
	if err := writer.(verifiedWriter).SaveVerified(ctx, uri, "link", strings.NewReader("new"), 3, sftpTestHash("new")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(root, "link"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("link replaced")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "new" {
		t.Fatalf("target: %q", data)
	}
	reader, _ := GetReader(ctx, uri, profile)
	r, _, err := reader.Open(ctx, uri, "link")
	if err != nil {
		t.Fatal(err)
	}
	data, err = io.ReadAll(r)
	r.Close()
	if err != nil || string(data) != "new" {
		t.Fatalf("link read: %q %v", data, err)
	}
	if err := writer.Save(ctx, uri, "dirlink/escape", strings.NewReader("x"), 1); err == nil {
		t.Fatal("traversed directory link")
	}
	if err := writer.Delete(ctx, uri, "link"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "link")); !os.IsNotExist(err) {
		t.Fatal("link not removed")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("target removed")
	}
}

func TestSFTPAuthenticationAndCancellation(t *testing.T) {
	uri, profile, _ := testSFTPServer(t)
	bad := *profile
	bad.KnownHostsFile = filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(bad.KnownHostsFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	scanner, err := GetScanner(context.Background(), uri, &bad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.Scan(context.Background(), uri, nil); err == nil {
		t.Fatal("accepted unknown host")
	}
	bad = *profile
	bad.Username = "wrong-user"
	scanner, _ = GetScanner(context.Background(), uri, &bad)
	if _, err := scanner.Scan(context.Background(), uri, nil); err == nil {
		t.Fatal("accepted wrong user")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scanner, _ = GetScanner(ctx, uri, profile)
	if _, err := scanner.Scan(ctx, uri, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	// A TCP server that never responds to SSH must be interruptible by context.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := listener.Accept()
		if err == nil {
			defer c.Close()
			_, _ = io.Copy(io.Discard, c)
		}
	}()
	stalled := "sftp://" + listener.Addr().String() + "/root"
	scanner, _ = GetScanner(context.Background(), stalled, profile)
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := scanner.Scan(ctx, stalled, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked handshake cancellation: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connection leaked")
	}
}

func TestSFTPProfilesRemainLocal(t *testing.T) {
	local := config.StorageProfileConfig{Type: "sftp", PrivateKeyFile: "local-key"}
	merged := mergeStorageProfiles(map[string]config.StorageProfileConfig{"shared": local}, map[string]config.StorageProfileConfig{"shared": {AccessKeyID: "remote"}})
	if merged["shared"].Type != "sftp" || merged["shared"].PrivateKeyFile != "local-key" {
		t.Fatal("coordinator replaced local SSH identity")
	}
	if _, err := s3Provider.Client(context.Background(), &local); err == nil {
		t.Fatal("SFTP profile accepted for S3")
	}
}

func TestSFTPWatcherRecovery(t *testing.T) {
	uri, profile, root := testSFTPServer(t)
	scanner, _ := GetScanner(context.Background(), uri, profile)
	watcher := &SFTPWatcher{scanner: scanner, interval: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes, errs, err := watcher.Watch(ctx, uri, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case ch := <-changes:
		if ch.ChangeType != FileChangeTypeCreated || ch.State.Hash != sftpTestHash("hello") {
			t.Fatalf("change: %+v", ch)
		}
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("no create")
	}
	if err := os.Rename(root, root+"-gone"); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Rename(root+"-gone", root) }()
	select {
	case <-errs:
	case ch := <-changes:
		t.Fatalf("unavailable root emitted %+v", ch)
	case <-time.After(3 * time.Second):
		t.Fatal("no scan error")
	}
	if err := os.Rename(root+"-gone", root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "file")); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ch := <-changes:
			if ch.ChangeType != FileChangeTypeDeleted {
				t.Fatalf("change: %+v", ch)
			}
			cancel()
			return
		case <-errs:
		case <-deadline:
			t.Fatal("no recovered delete")
		}
	}
}

func TestSFTPWriterRequiresAtomicRename(t *testing.T) {
	// Test servers advertise extensions globally; these tests are not parallel.
	if err := sftp.SetSFTPExtensions(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sftp.SetSFTPExtensions("hardlink@openssh.com", "posix-rename@openssh.com", "statvfs@openssh.com")
	})
	uri, profile, root := testSFTPServer(t)
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	writer, _ := GetWriter(context.Background(), uri, profile)
	err := writer.(verifiedWriter).SaveVerified(context.Background(), uri, "file", strings.NewReader("new"), 3, sftpTestHash("new"))
	if err == nil || !strings.Contains(err.Error(), "posix-rename") {
		t.Fatalf("expected capability rejection, got %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "file"))
	if string(data) != "old" {
		t.Fatal("destination was modified")
	}
}

func TestSFTPNodeTransferBothDirections(t *testing.T) {
	uri, profile, remoteRoot := testSFTPServer(t)
	localRoot := t.TempDir()
	content := "replicated content"
	ctx := context.Background()
	for _, toRemote := range []bool{true, false} {
		sourceURI, destURI := localRoot, uri
		var sourceProfile *config.StorageProfileConfig
		destProfile := profile
		sourceRoot, destRoot := localRoot, remoteRoot
		if !toRemote {
			sourceURI, destURI = uri, localRoot
			sourceProfile, destProfile = profile, nil
			sourceRoot, destRoot = remoteRoot, localRoot
		}
		if err := os.WriteFile(filepath.Join(sourceRoot, "transfer.txt"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		reader, err := GetReader(ctx, sourceURI, sourceProfile)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			r, _, err := reader.Open(req.Context(), sourceURI, "transfer.txt")
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			defer r.Close()
			_, _ = io.Copy(w, r)
		}))
		client, err := apiclient.New(config.Config{App: config.AppConfig{NodeID: "node", NodeAddress: server.URL, CoordinatorURL: server.URL}, Auth: config.AuthConfig{NodeSecret: "test"}})
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		runtime := &Runtime{client: client}
		writer, err := GetWriter(ctx, destURI, destProfile)
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		pending := apiclient.ReplicaInventoryFile{FileID: 1, RelativeURI: "transfer.txt", InventoryVersion: 1, Size: int64(len(content)), Hash: sftpTestHash(content)}
		err = runtime.transferAndSaveReplicaFile(ctx, writer, apiclient.Replica{URI: destURI}, server.URL, 1, pending, "test-token")
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(destRoot, "transfer.txt"))
		if err != nil || string(data) != content {
			t.Fatalf("transfer toRemote=%v: %q %v", toRemote, data, err)
		}
	}
	runtime := &Runtime{storageProfiles: map[string]config.StorageProfileConfig{"test": *profile}}
	source, err := runtime.thumbnailSource(ctx, apiclient.Replica{URI: uri, StorageProfile: "test"}, apiclient.ReplicaInventoryFile{RelativeURI: "transfer.txt", Size: int64(len(content))})
	if err != nil {
		t.Fatal(err)
	}
	r, err := source.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	r.Close()
	if err != nil || string(data) != content || source.IsLocalFile() {
		t.Fatalf("thumbnail source: %q %v", data, err)
	}
}

func TestSFTPRelativeLinkChain(t *testing.T) {
	uri, profile, root := testSFTPServer(t)
	external := t.TempDir()
	if err := os.Mkdir(filepath.Join(external, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(external, "target"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	// '..' must be interpreted after following the preceding directory link.
	if err := os.Symlink(filepath.Join(external, "child"), filepath.Join(root, "directory")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("directory/../target", filepath.Join(root, "first")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("first", filepath.Join(root, "second")); err != nil {
		t.Fatal(err)
	}
	writer, _ := GetWriter(context.Background(), uri, profile, true)
	if err := writer.(verifiedWriter).SaveVerified(context.Background(), uri, "second", strings.NewReader("new"), 3, sftpTestHash("new")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(external, "target"))
	if err != nil || string(data) != "new" {
		t.Fatalf("target=%q %v", data, err)
	}
	for _, name := range []string{"first", "second"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("link %s replaced", name)
		}
	}
}

func TestSFTPWatcherStopsWhenOptionsChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	replica := apiclient.Replica{ID: 1, URI: "sftp://host/root", Type: "storage", Status: "active", StorageProfile: "new", FollowSymlinks: true}
	runtime := &Runtime{replicas: []apiclient.Replica{replica}, watchers: map[uint]*runningReplicaWatcher{1: {storageProfile: "old", cancel: cancel}}}
	runtime.stopDeletedReplicaWatchers()
	if ctx.Err() == nil || runtime.replicaWatcherExists(1) {
		t.Fatal("old SFTP watcher survived profile change")
	}
	if !replicaFollowsSymlinks(replica) {
		t.Fatal("SFTP follow_symlinks lost")
	}
}
