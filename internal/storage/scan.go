package storage

import (
	"context"
	"sort"
	"time"
)

const (
	HashAlgorithmBLAKE3 = "blake3"
	HashAlgorithmS3ETag = "s3-etag"
)

const (
	FileChangeTypeCreated        = "created"
	FileChangeTypeModified       = "modified"
	FileChangeTypeDeleted        = "deleted"
	FileChangeTypeRenamed        = "renamed"
	FileChangeTypeUnknown        = "unknown"
	FileChangeTypeRescanRequired = "rescan_required"
)

type FileState struct {
	RelativeURI   string
	Size          int64
	Hash          string
	HashAlgorithm string
	Created       time.Time
	Modified      time.Time
}

type FileChange struct {
	RelativeURI         string
	ChangeType          string
	PreviousRelativeURI *string
	State               *FileState
}

type Scanner interface {
	Scan(ctx context.Context, rootURI string) ([]FileState, error)
}

type Watcher interface {
	Watch(ctx context.Context, rootURI string) (<-chan FileChange, <-chan error, error)
}

func sortFileStates(states []FileState) {
	sort.Slice(states, func(i, j int) bool {
		return states[i].RelativeURI < states[j].RelativeURI
	})
}

func compareSnapshots(previous, current []FileState) []FileChange {
	previousByURI := make(map[string]FileState, len(previous))
	for _, state := range previous {
		previousByURI[state.RelativeURI] = state
	}

	currentByURI := make(map[string]FileState, len(current))
	for _, state := range current {
		currentByURI[state.RelativeURI] = state
	}

	changes := make([]FileChange, 0)
	for _, state := range current {
		prev, ok := previousByURI[state.RelativeURI]
		if !ok {
			stateCopy := state
			changes = append(changes, FileChange{
				RelativeURI: state.RelativeURI,
				ChangeType:  FileChangeTypeCreated,
				State:       &stateCopy,
			})
			continue
		}

		if prev.Size != state.Size || prev.Modified != state.Modified || prev.Hash != state.Hash || prev.HashAlgorithm != state.HashAlgorithm {
			stateCopy := state
			changes = append(changes, FileChange{
				RelativeURI: state.RelativeURI,
				ChangeType:  FileChangeTypeModified,
				State:       &stateCopy,
			})
		}
	}

	for _, state := range previous {
		if _, ok := currentByURI[state.RelativeURI]; ok {
			continue
		}
		changes = append(changes, FileChange{
			RelativeURI: state.RelativeURI,
			ChangeType:  FileChangeTypeDeleted,
		})
	}

	sort.Slice(changes, func(i, j int) bool {
		if changes[i].RelativeURI == changes[j].RelativeURI {
			return changes[i].ChangeType < changes[j].ChangeType
		}
		return changes[i].RelativeURI < changes[j].RelativeURI
	})

	return changes
}
