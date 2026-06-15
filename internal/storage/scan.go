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
	FileChangeTypeCreated        = "created"
	FileChangeTypeModified       = "modified"
	FileChangeTypeDeleted        = "deleted"
	FileChangeTypeRenamed        = "renamed"
	FileChangeTypeUnknown        = "unknown"
	FileChangeTypeRescanRequired = "rescan_required"
)

type FileState struct {
	RelativeURI string
	Size        int64
	Hash        string
	Created     time.Time
	Modified    time.Time
}

type FileChange struct {
	RelativeURI         string
	ChangeType          string
	PreviousRelativeURI *string
	State               *FileState
}

type Scanner interface {
	Scan(ctx context.Context, rootURI string, oldStates map[string]FileState, targetRelativeURI ...string) ([]FileState, error)
}

type Watcher interface {
	Watch(ctx context.Context, rootURI string, targetRelativeURIs []string) (<-chan FileChange, <-chan error, error)
}

type Reader interface {
	Open(ctx context.Context, replicaURI string, relativeURI string) (io.ReadCloser, int64, error)
}

type Writer interface {
	Save(ctx context.Context, replicaURI string, relativeURI string, content io.Reader, size int64) error
	Delete(ctx context.Context, replicaURI string, relativeURI string) error
}

func sortFileStates(states []FileState) {
	sort.Slice(states, func(i, j int) bool {
		return states[i].RelativeURI < states[j].RelativeURI
	})
}

func cleanRelativeURISet(relativeURIs []string) (map[string]struct{}, error) {
	if len(relativeURIs) == 0 {
		return nil, nil
	}

	result := make(map[string]struct{}, len(relativeURIs))
	for _, relativeURI := range relativeURIs {
		clean, err := cleanWriteRelativeURI(relativeURI)
		if err != nil {
			return nil, err
		}
		result[clean] = struct{}{}
	}
	return result, nil
}

func oldStateWithMatchingMetadata(oldStates map[string]FileState, relativeURI string, size int64, modified time.Time) (FileState, bool) {
	if oldStates == nil {
		return FileState{}, false
	}
	oldState, ok := oldStates[relativeURI]
	if !ok || oldState.Hash == "" {
		return FileState{}, false
	}
	return oldState, oldState.Size == size && oldState.Modified.Equal(modified)
}

// turn list of FileState to map by uri
func fileStateMap(states []FileState) map[string]FileState {
	if len(states) == 0 {
		return nil
	}
	result := make(map[string]FileState, len(states))
	for _, state := range states {
		result[state.RelativeURI] = state
	}
	return result
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

		if prev.Size != state.Size || prev.Modified != state.Modified || prev.Hash != state.Hash {
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

func GetReader(ctx context.Context, uri string) (Reader, error) {
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
		return NewS3Reader(client), nil

	case "file", "":
		return NewFilesystemReader(), nil

	default:
		return nil, errTransferUnsupportedURI
	}
}
