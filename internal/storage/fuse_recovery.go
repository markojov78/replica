//go:build linux && !arm

package storage

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type fuseMountInfo struct {
	fsType  string
	source  string
	options string
}

// Read the kernel table without traversing potentially disconnected mounts.
func readFuseMounts() (map[string]fuseMountInfo, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseFuseMounts(f)
}

func parseFuseMounts(r io.Reader) (map[string]fuseMountInfo, error) {
	mounts := make(map[string]fuseMountInfo)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), " - ", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid mountinfo entry")
		}
		left, right := strings.Fields(parts[0]), strings.Fields(parts[1])
		if len(left) < 6 || len(right) < 3 {
			return nil, fmt.Errorf("invalid mountinfo fields")
		}
		path := unescape.Replace(left[4])
		if _, exists := mounts[path]; exists {
			// Stacked mounts have ambiguous ownership. Never automatically remove them.
			mounts[path] = fuseMountInfo{}
		} else {
			mounts[path] = fuseMountInfo{fsType: right[0], source: unescape.Replace(right[1]), options: right[2]}
		}
	}
	return mounts, scanner.Err()
}

func (info fuseMountInfo) ownedReplicaMount() bool {
	if info.source != "replica-share" || (info.fsType != "fuse" && !strings.HasPrefix(info.fsType, "fuse.")) {
		return false
	}
	owner := "user_id=" + strconv.Itoa(os.Geteuid())
	for _, option := range strings.Split(info.options, ",") {
		if option == owner {
			return true
		}
	}
	return false
}

func (m *fuseManager) preparePrefix(ctx context.Context) error {
	if m.prefixLock != nil {
		return nil
	}
	if err := os.MkdirAll(m.cfg.PathPrefix, 0755); err != nil {
		return err
	}
	prefix, err := filepath.EvalSymlinks(m.cfg.PathPrefix)
	if err != nil {
		return err
	}
	// Never unlink this file: all processes must lock the same inode.
	fd, err := syscall.Open(filepath.Join(prefix, ".replica-fuse.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	lock := os.NewFile(uintptr(fd), filepath.Join(prefix, ".replica-fuse.lock"))
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return fmt.Errorf("FUSE prefix %s is already in use or cannot be locked: %w", prefix, err)
	}
	m.prefixLock = lock
	mounts, err := readFuseMounts()
	if err != nil {
		lock.Close()
		m.prefixLock = nil
		return err
	}
	for path, info := range mounts {
		if filepath.Dir(path) != prefix || !info.ownedReplicaMount() {
			continue
		}
		if err := m.recoverMountPoint(ctx, path); err != nil {
			log.Printf("storage runtime fuse startup recovery skipped path=%s: %v", path, err)
		}
	}
	return nil
}

func (m *fuseManager) recoverMountPoint(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mounts, err := readFuseMounts()
	if err != nil {
		return err
	}
	info, exists := mounts[path]
	if !exists {
		return nil
	}
	if m.prefixLock == nil || filepath.Dir(path) != filepath.Dir(m.prefixLock.Name()) || !info.ownedReplicaMount() {
		return fmt.Errorf("mount is not exclusively managed by this Replica instance")
	}
	_, err = os.Stat(path)
	if !errors.Is(err, syscall.ENOTCONN) {
		return fmt.Errorf("existing mount is not confirmed disconnected (stat: %v)", err)
	}
	helper, err := exec.LookPath("fusermount3")
	if err != nil {
		helper, err = exec.LookPath("fusermount")
	}
	if err != nil {
		return err
	}
	unmountCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(unmountCtx, helper, "-u", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("unmount stale mount: %w: %s", err, strings.TrimSpace(string(output)))
	}
	mounts, err = readFuseMounts()
	if err != nil {
		return err
	}
	if _, exists := mounts[path]; exists {
		return fmt.Errorf("mount remains after unmount")
	}
	log.Printf("storage runtime fuse recovered stale mount path=%s", path)
	return nil
}
