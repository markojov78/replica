package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"replica/internal/apiclient"
	"replica/internal/config"
)

var errRemovableUnavailable = errors.New("removable media unavailable")

// Empty means no directory entries, not no inventory files. In particular,
// a directory containing only subdirectories is still present. Temporary
// replication writes must not make an otherwise empty root appear mounted.
func removableRootInfo(uri string) (os.FileInfo, error) {
	root, err := localFilesystemPath(uri)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errRemovableUnavailable, err)
	}
	dir, err := os.Open(root)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errRemovableUnavailable, err)
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: root is not a readable directory: %s", errRemovableUnavailable, root)
	}
	for {
		entries, err := dir.Readdirnames(32)
		for _, entry := range entries {
			if !isTemporaryWritePath(entry) {
				return info, nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("%w: empty root: %s", errRemovableUnavailable, root)
			}
			return nil, fmt.Errorf("%w: %v", errRemovableUnavailable, err)
		}
	}
}

func checkReplicaAvailable(replica apiclient.Replica) error {
	if replica.Type != "removable" {
		return nil
	}
	_, err := removableRootInfo(replica.URI)
	return err
}

func openRemovableRoot(uri string) (*os.Root, os.FileInfo, error) {
	info, err := removableRootInfo(uri)
	if err != nil {
		return nil, nil, err
	}
	rootPath, err := localFilesystemPath(uri)
	if err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errRemovableUnavailable, err)
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, nil, fmt.Errorf("%w: root changed while opening", errRemovableUnavailable)
	}
	return root, info, nil
}

func checkRemovableRootUnchanged(uri string, before os.FileInfo, requireNonempty bool) error {
	var after os.FileInfo
	var err error
	if requireNonempty {
		after, err = removableRootInfo(uri)
	} else {
		var rootPath string
		rootPath, err = localFilesystemPath(uri)
		if err == nil {
			after, err = os.Stat(rootPath)
		}
	}
	if err != nil {
		return fmt.Errorf("%w: %v", errRemovableUnavailable, err)
	}
	if !os.SameFile(before, after) {
		return fmt.Errorf("%w: root changed during operation", errRemovableUnavailable)
	}
	return nil
}

func scanReplicaStates(ctx context.Context, replica apiclient.Replica, scanner Scanner, old map[string]FileState, targets ...string) ([]FileState, error) {
	if replica.Type != "removable" {
		return scanner.Scan(ctx, replica.URI, old, targets...)
	}
	before, err := removableRootInfo(replica.URI)
	if err != nil {
		return nil, err
	}
	states, err := scanner.Scan(ctx, replica.URI, old, targets...)
	if err != nil {
		return nil, err
	}
	after, err := removableRootInfo(replica.URI)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) {
		return nil, fmt.Errorf("%w: root changed during scan", errRemovableUnavailable)
	}
	return states, nil
}

func replicaWriter(ctx context.Context, replica apiclient.Replica, profile *config.StorageProfileConfig) (Writer, error) {
	if replica.Type == "removable" {
		return &FilesystemWriter{removable: true}, nil
	}
	return GetWriter(ctx, replica.URI, profile, replicaFollowsSymlinks(replica))
}
