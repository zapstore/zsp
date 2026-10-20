package fixture

// Hotbox returns a hotbox executable. HOTBOX_BINARY skips the build; otherwise
// HOTBOX_DIR must point at the hotbox repository root.
func Hotbox() (path string, cleanup func(), err error) {
	return binaryFromRepo("HOTBOX", "hotbox", "", ".")
}
