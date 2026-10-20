//go:build release && !race

// Package release drives the hotbox and zsp binaries against a temporary relay.
//
// Required for TestMain:
//   - RELAY_BINARY or RELAY_DIR
//   - HOTBOX_BINARY or HOTBOX_DIR
//
// ZSP is built from the enclosing zsp module when ZSP_BINARY and ZSP_DIR are
// unset. Set them to test another checkout or a prebuilt binary.
//
// Required for APK-building tests:
//   - ANDROID_HOME or ANDROID_SDK_ROOT
//
// make test-release sets RELAY_DIR and HOTBOX_DIR for a typical monorepo layout.
//
//	make test-release
package release
