//go:build publish && !race

// Package publish drives a local relay and publishes through ZSP.
//
// Required for TestMain:
//   - RELAY_BINARY or RELAY_DIR
//
// Required for APK-building tests:
//   - ANDROID_HOME or ANDROID_SDK_ROOT
//
// make test-integration sets RELAY_DIR when ../relay exists.
//
//	make test-integration
package publish
