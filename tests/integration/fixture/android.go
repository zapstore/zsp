package fixture

import (
	"fmt"
	"os"
	"strings"
)

// AndroidSDKRoot returns ANDROID_HOME or ANDROID_SDK_ROOT.
func AndroidSDKRoot() (string, error) {
	for _, key := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if root := strings.TrimSpace(os.Getenv(key)); root != "" {
			return root, nil
		}
	}
	return "", fmt.Errorf("set ANDROID_HOME or ANDROID_SDK_ROOT to the Android SDK root")
}
