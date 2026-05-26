package storage

import (
	"os"
	"time"
)

func fileBirthTime(_ os.FileInfo) (time.Time, bool) {
	return time.Time{}, false
}
