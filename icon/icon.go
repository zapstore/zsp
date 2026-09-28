// Package icon reads an Android launcher icon from an APK.
package icon

import (
	"fmt"

	"github.com/zapstore/zsp/internal/apk"
)

// Icon returns the launcher icon as PNG bytes.
// A missing icon returns nil, nil. A bad APK returns an error.
func Icon(path string) ([]byte, error) {
	info, err := apk.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("icon: %w", err)
	}
	return info.Icon, nil
}
