package release

import (
	"path/filepath"
	"testing"
)

func TestIconMissingFile(t *testing.T) {
	_, err := Icon(t.Context(), filepath.Join(t.TempDir(), "missing.apk"))
	if err == nil {
		t.Fatal("expected error")
	}
}
