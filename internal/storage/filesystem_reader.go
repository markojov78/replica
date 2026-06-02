package storage

import (
	"context"
	"io"
	"os"
)

type FilesystemReader struct{}

func NewFilesystemReader() *FilesystemReader {
	return &FilesystemReader{}
}

func (r *FilesystemReader) Open(ctx context.Context, replicaURI string, relativeURI string) (io.ReadCloser, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}

	localPath, err := resolveReplicaFilePath(replicaURI, relativeURI)
	if err != nil {
		return nil, 0, err
	}

	info, err := os.Stat(localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, errTransferFileNotFound
		}
		return nil, 0, err
	}
	if info.IsDir() {
		return nil, 0, errTransferFileNotFound
	}

	file, err := os.Open(localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, errTransferFileNotFound
		}
		return nil, 0, err
	}
	return file, info.Size(), nil
}
