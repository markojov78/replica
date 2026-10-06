package storage

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"
	"github.com/zeebo/blake3"
)

var errSFTPDirectoryLink = errors.New("SFTP directory symlinks are ignored")

type SFTPScanner struct {
	connector      *sftpConnector
	followSymlinks bool
}

func (s *SFTPScanner) Scan(ctx context.Context, uri string, old map[string]FileState, targets ...string) ([]FileState, error) {
	targetSet, err := cleanRelativeURISet(targets)
	if err != nil {
		return nil, err
	}
	c, root, err := s.connector.connect(ctx, uri)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if err := sftpRoot(c.Client, root); err != nil {
		return nil, err
	}
	var states []FileState
	add := func(rel string) error {
		state, err := sftpFileState(ctx, c.Client, root, rel, s.followSymlinks, old)
		if err != nil {
			return err
		}
		if state != nil {
			states = append(states, *state)
		}
		return nil
	}
	if len(targetSet) > 0 {
		for rel := range targetSet {
			if sftpTemporaryPath(rel) {
				continue
			}
			if err := sftpParents(c.Client, root, rel, false); err != nil {
				if os.IsNotExist(err) || errors.Is(err, errSFTPDirectoryLink) {
					continue
				}
				return nil, err
			}
			if err := add(rel); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
		}
	} else {
		var walk func(string) error
		walk = func(rel string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			entries, err := c.ReadDir(path.Join(root, rel))
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if entry.Name() == "." || entry.Name() == ".." || strings.Contains(entry.Name(), "/") {
					return fmt.Errorf("invalid SFTP directory entry")
				}
				child := path.Join(rel, entry.Name())
				if sftpTemporaryPath(child) {
					continue
				}
				if entry.IsDir() {
					if err := walk(child); err != nil {
						return err
					}
				} else if err := add(child); err != nil {
					return err
				}
			}
			return nil
		}
		if err := walk(""); err != nil {
			return nil, err
		}
	}
	// A lost root must never become a successful empty snapshot.
	if err := sftpRoot(c.Client, root); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sortFileStates(states)
	return states, nil
}

func sftpRoot(c *sftp.Client, root string) error {
	info, err := c.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("SFTP replica root must be a directory")
	}
	return nil
}

// Directory symlinks are not traversed, even when file symlinks are enabled.
func sftpParents(c *sftp.Client, root, rel string, create bool) error {
	current := root
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		current = path.Join(current, part)
		info, err := c.Lstat(current)
		if os.IsNotExist(err) && create {
			if err = c.Mkdir(current); err != nil {
				return err
			}
			info, err = c.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errSFTPDirectoryLink
		}
		if !info.IsDir() {
			return fmt.Errorf("SFTP parent is not a directory: %s", current)
		}
	}
	return nil
}

func sftpTemporaryPath(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if isTemporaryWritePath(part) {
			return true
		}
	}
	return false
}

func sftpFileState(ctx context.Context, c *sftp.Client, root, rel string, follow bool, old map[string]FileState) (*FileState, error) {
	name := path.Join(root, rel)
	info, err := c.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if !follow {
			return nil, nil
		}
		info, err = c.Stat(name)
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}
	if !info.Mode().IsRegular() {
		return nil, nil
	}
	modified := info.ModTime().UTC()
	state := &FileState{RelativeURI: rel, Size: info.Size(), Created: modified, Modified: modified}
	if prev, ok := oldStateWithMatchingMetadata(old, rel, info.Size(), modified); ok {
		state.Hash = prev.Hash
		return state, nil
	}
	f, err := c.Open(name)
	if err != nil {
		return nil, err
	}
	hasher := blake3.New()
	written, err := copyWithContext(ctx, hasher, f)
	state.Hash = hex.EncodeToString(hasher.Sum(nil))
	after, statErr := f.Stat()
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if statErr != nil {
		return nil, statErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	current, err := c.Stat(name)
	if err != nil {
		return nil, err
	}
	if written != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) {
		return nil, fmt.Errorf("SFTP file changed while hashing: %s", rel)
	}
	return state, nil
}
