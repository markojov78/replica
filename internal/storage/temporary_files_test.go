package storage

import "testing"

func TestIsTemporaryWritePathMatchesBasenameOnly(t *testing.T) {
	for _, value := range []string{
		TemporaryWritePrefix + "root",
		"nested/" + TemporaryWritePrefix + "nested",
		`nested\` + TemporaryWritePrefix + "windows",
	} {
		if !isTemporaryWritePath(value) {
			t.Fatalf("isTemporaryWritePath(%q) = false, want true", value)
		}
	}
	for _, value := range []string{
		"visible.txt",
		TemporaryWritePrefix[:len(TemporaryWritePrefix)-1],
		"nested/" + TemporaryWritePrefix + "dir/file.txt",
	} {
		if isTemporaryWritePath(value) {
			t.Fatalf("isTemporaryWritePath(%q) = true, want false", value)
		}
	}
}
