package zsp

import (
	"path/filepath"
	"testing"
)

func TestIconMissingFile(t *testing.T) {
	_, err := Icon(filepath.Join(t.TempDir(), "missing.apk"))
	if err == nil {
		t.Fatal("expected error")
	}
}
