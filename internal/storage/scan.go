package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
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

type Writer interface {
	Save(ctx context.Context, replicaURI string, relativeURI string, content io.Reader) error
	Delete(ctx context.Context, replicaURI string, relativeURI string) error
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

var s3Provider = &S3ClientProvider{}

// Scanner factory to resolve scanner implementation from uri scheme
func GetScanner(ctx context.Context, uri string) (Scanner, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}

	switch u.Scheme {
	case "s3":
		s3client, err := s3Provider.Client(ctx)
		if err != nil {
			return nil, err
		}
		return NewS3Scanner(s3client), nil
	case "file", "": // plain local path
		return NewFilesystemScanner(), nil
	default:
		return nil, fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}
}

func GetWatcher(ctx context.Context, uri string) (Watcher, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}

	switch u.Scheme {
	case "s3":
		client, err := s3Provider.Client(ctx)
		if err != nil {
			return nil, err
		}
		interval := 5 * time.Minute // TODO make configurable per-replica with reasonable default in config

		scanner := NewS3Scanner(client)
		return NewS3Watcher(scanner, interval), nil

	case "file", "":
		return NewFilesystemWatcher(), nil

	default:
		return nil, fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}
}

func GetWriter(ctx context.Context, uri string) (Writer, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}

	switch u.Scheme {
	case "s3":
		client, err := s3Provider.Client(ctx)
		if err != nil {
			return nil, err
		}
		return NewS3Writer(client), nil

	case "file", "":
		return NewFilesystemWriter(), nil

	default:
		return nil, fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}
}
