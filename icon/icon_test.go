package icon

import "testing"

func TestIconMissingFile(t *testing.T) {
	if _, err := Icon("missing.apk"); err == nil {
		t.Fatal("expected error")
	}
}
