package main

// `odo preview-setup` — the explicit, network-allowed provisioning phase
// for /preview (2026-08-25 review P1). Captures run the pinned playwright
// CLI only-if-cached and never fetch registry code with daemon
// privileges; a cold machine provisions here, at the user's hand.

import (
	"fmt"
	"os"

	"github.com/yingliang-zhang/odo/internal/ipc"
)

// runPreviewSetupCLI provisions the pinned playwright CLI + chromium.
func runPreviewSetupCLI(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: odo preview-setup")
		return 2
	}
	return ipc.PreviewSetup(os.Stdout, os.Stderr)
}
