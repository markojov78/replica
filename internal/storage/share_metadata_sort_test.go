package storage

import (
	"reflect"
	"testing"
	"time"

	"replica/internal/apiclient"
)

func TestSortShareFilesByMetadata(t *testing.T) {
	base := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
	files := []apiclient.ReplicaInventoryFile{
		{FileID: 1, Created: base.Add(3 * time.Hour)},
		{FileID: 2, Created: base, Metadata: map[string]string{"capture_time": "2024-05-01T05:00:00", "capture_offset": "+02:00", "duration_ms": "900"}},
		{FileID: 3, Created: base, Metadata: map[string]string{"capture_time": "2024-05-01T01:00:00", "duration_ms": "100"}},
	}
	order := func(sortBy, direction string) []uint {
		sorted := append([]apiclient.ReplicaInventoryFile(nil), files...)
		sortShareFiles(sorted, ShareFileListFilter{Sort: sortBy, Order: direction})
		ids := make([]uint, 0, len(sorted))
		for _, file := range sorted {
			ids = append(ids, file.FileID)
		}
		return ids
	}

	// File 2 was taken at 03:00 UTC, file 1 falls back to its 03:00 created time and keeps its stable position.
	if got := order("taken", "asc"); !reflect.DeepEqual(got, []uint{3, 1, 2}) {
		t.Fatalf("taken asc = %v", got)
	}
	if got := order("duration", "asc"); !reflect.DeepEqual(got, []uint{3, 2, 1}) {
		t.Fatalf("duration asc = %v", got)
	}
	if got := order("duration", "desc"); !reflect.DeepEqual(got, []uint{2, 3, 1}) {
		t.Fatalf("duration desc = %v", got)
	}
}
