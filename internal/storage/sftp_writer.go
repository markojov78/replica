package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"
	"github.com/zeebo/blake3"
)

type SFTPWriter struct {
	connector      *sftpConnector
	followSymlinks bool
}

func (w *SFTPWriter) Save(ctx context.Context, uri, rel string, content io.Reader, size int64) error {
	return w.save(ctx, uri, rel, content, size, "", false)
}
func (w *SFTPWriter) SaveVerified(ctx context.Context, uri, rel string, content io.Reader, size int64, hash string) error {
	return w.save(ctx, uri, rel, content, size, hash, true)
}
func (w *SFTPWriter) save(ctx context.Context, uri, rel string, content io.Reader, size int64, hash string, verify bool) error {
	rel, err := cleanWriteRelativeURI(rel)
	if err != nil {
		return err
	}
	c, root, err := w.connector.connect(ctx, uri)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := sftpRoot(c.Client, root); err != nil {
		return err
	}
	if _, ok := c.HasExtension("posix-rename@openssh.com"); !ok {
		return fmt.Errorf("SFTP writes require posix-rename@openssh.com")
	}
	if err := sftpParents(c.Client, root, rel, true); err != nil {
		return err
	}
	target := path.Join(root, rel)
	if w.followSymlinks {
		info, err := c.Lstat(target)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			target, err = resolveSFTPLink(c.Client, target)
			if err != nil {
				return err
			}
			info, err = c.Stat(target)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("SFTP symlink target is not a regular file")
			}
		}
	}
	temp := path.Join(path.Dir(target), TemporaryWritePrefix+rand.Text())
	f, err := c.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	defer c.Remove(temp)
	hasher := blake3.New()
	var dst io.Writer = f
	if verify {
		dst = io.MultiWriter(f, hasher)
	}
	written, copyErr := copyWithContext(ctx, dst, content)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if verify {
		actual := hex.EncodeToString(hasher.Sum(nil))
		if written != size || actual != hash {
			return fmt.Errorf("%w: expected size=%d hash=%s, got size=%d hash=%s", ErrFileIntegrityMismatch, size, hash, written, actual)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := sftpRoot(c.Client, root); err != nil {
		return err
	}
	return c.PosixRename(temp, target)
}
func (w *SFTPWriter) Delete(ctx context.Context, uri, rel string) error {
	rel, err := cleanWriteRelativeURI(rel)
	if err != nil {
		return err
	}
	c, root, err := w.connector.connect(ctx, uri)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := sftpRoot(c.Client, root); err != nil {
		return err
	}
	if err := sftpParents(c.Client, root, rel, false); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	err = c.Remove(path.Join(root, rel))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Resolve components explicitly: some servers' REALPATH implementations do not
// dereference symlinks. Renaming onto an unresolved link would destroy the link.
func resolveSFTPLink(c *sftp.Client, name string) (string, error) {
	remaining := strings.Split(name, "/")
	resolved := "/"
	links := 0
	for len(remaining) > 0 {
		part := remaining[0]
		remaining = remaining[1:]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			resolved = path.Dir(resolved)
			continue
		}
		next := path.Join(resolved, part)
		info, err := c.Lstat(next)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = next
			continue
		}
		links++
		if links > 40 {
			return "", fmt.Errorf("too many SFTP symbolic links")
		}
		target, err := c.ReadLink(next)
		if err != nil {
			return "", err
		}
		if path.IsAbs(target) {
			resolved = "/"
		}
		remaining = append(strings.Split(target, "/"), remaining...)
	}
	return resolved, nil
}
