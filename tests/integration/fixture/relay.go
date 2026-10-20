package fixture

// Relay returns a relay executable. RELAY_BINARY skips the build; otherwise
// RELAY_DIR must point at the relay repository root (directory with cmd/).
func Relay() (path string, cleanup func(), err error) {
	return binaryFromRepo("RELAY", "relay", "", "./cmd")
}
