package zsp

import "github.com/zapstore/zsp/internal/apk"

// Icon returns the launcher icon extracted from an APK, as PNG bytes.
// A missing icon is nil, nil.
func Icon(path string) ([]byte, error) {
	info, err := apk.Parse(path)
	if err != nil {
		return nil, err
	}
	if len(info.Icon) == 0 {
		return nil, nil
	}
	return append([]byte(nil), info.Icon...), nil
}
