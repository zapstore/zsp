package release

import (
	"context"

	"github.com/zapstore/zsp/v2/internal/apk"
)

// Icon returns the launcher icon extracted from an APK, as PNG bytes.
// A missing icon is nil, nil.
func Icon(ctx context.Context, path string) ([]byte, error) {
	info, err := apk.Parse(ctx, path)
	if err != nil {
		if contextErr := contextOperationError(ctx.Err(), "read APK"); contextErr != nil {
			return nil, contextErr
		}
		return nil, wrapOperationError(ErrInvalidAPK, err, false, "read APK"+causeDetail(err))
	}
	if len(info.Icon) == 0 {
		return nil, nil
	}
	return append([]byte(nil), info.Icon...), nil
}
