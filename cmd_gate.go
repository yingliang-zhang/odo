package main

import (
	"fmt"
	"os"

	"github.com/yingliang-zhang/odo/internal/ipc"
)

// 2026-08-27 (D1 control-plane hardening lock, ruling ②): `odo gate
// re-pin` — the HUMAN re-acknowledgment of the Tier-0 gate core. A human
// edit of internal/ipc/gatepolicy.go or internal/ipc/gate_manifest.json
// IS the exemption grant (Tier-0 status is compiled in; no pipeline actor
// may land those files, attestation included), and the daemon proves the
// grant happened by comparing pinned hashes at boot: a mismatch latches
// gate_policy_drift and every landing pipeline refuses. Re-pin
// recomputes the sha16 of each Tier-0 file and rewrites the manifest;
// it NEVER commits — the human commits BOTH files, then restarts the
// daemon (the latch clears only at boot, by design: the judgment call is
// the re-pin + commit act, not a runtime flag).
//
//	odo gate re-pin

const gateUsage = `usage: odo gate re-pin
Recomputes the sha16 of each Tier-0 gate file (internal/ipc/gatepolicy.go,
internal/ipc/gate_manifest.json) and rewrites the pinned manifest. Never
commits: commit BOTH files yourself, then restart the daemon to release
the gate_policy_drift latch.`

// runGateCLI implements `odo gate <sub>` (today only re-pin).
func runGateCLI(args []string) int {
	if len(args) != 1 || args[0] != "re-pin" {
		fmt.Fprintln(os.Stderr, gateUsage)
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo gate re-pin: resolve cwd: %v\n", err)
		return 1
	}
	root, err := journalRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo gate re-pin: %v\n", err)
		return 1
	}
	pins, err := ipc.RepinGateManifest(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo gate re-pin: %v\n", err)
		return 1
	}
	fmt.Printf("re-pinned %s:\n", "internal/ipc/gate_manifest.json")
	for _, f := range []string{"internal/ipc/gatepolicy.go", "internal/ipc/gate_manifest.json"} {
		sha := pins[f]
		if sha == "" {
			sha = "(self — empty slot by construction)"
		}
		fmt.Printf("  %s  sha16=%s\n", f, sha)
	}
	fmt.Println("Now commit BOTH files:")
	fmt.Println("  git add internal/ipc/gatepolicy.go internal/ipc/gate_manifest.json && git commit")
	fmt.Println("Then restart the daemon (the drift latch clears only at boot).")
	return 0
}
