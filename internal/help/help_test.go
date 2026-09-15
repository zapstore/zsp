package help

import (
	"strings"
	"testing"
)

func TestRootHelp_ReferencesWizard(t *testing.T) {
	out := RootHelp()
	if !strings.Contains(out, "Start the interactive app setup wizard") {
		t.Error("RootHelp() should reference the wizard as the identity onboarding path")
	}
	if strings.Contains(out, "identity create") {
		t.Error("RootHelp() should not expose the removed identity command")
	}
}
