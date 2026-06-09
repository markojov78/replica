package storage

import (
	"path"
	"path/filepath"
	"strings"
)

const TemporaryWritePrefix = ".replica-write-"

func temporaryWritePattern() string {
	return TemporaryWritePrefix + "*"
}

func isTemporaryWritePath(value string) bool {
	normalized := strings.ReplaceAll(filepath.ToSlash(value), `\`, "/")
	base := path.Base(normalized)
	return strings.HasPrefix(base, TemporaryWritePrefix)
}
