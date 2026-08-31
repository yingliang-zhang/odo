# Gate Policy & Protected Paths

- Two-tier gate boundary: Tier-0 (gatepolicy.go, gate_manifest.json) is human-only — editing it IS the exemption grant; Tier-1 prefixes (internal/ipc, internal/store, internal/git, internal/moa, internal/adapter, root main.go) require unanimous panel attestation to auto-land (bug-fix-epoch-14)
- Startup drift latch: Tier-0 sha16 is recomputed at NewServer; mismatch journals gate_source_drift and refuses autoLand/loopFixPipeline/settle-valve until a human runs `odo gate re-pin`, which rewrites the manifest but never commits (bug-fix-epoch-14)
- Gate-source diffs never land via the majority valve: the majority_accept attestation path was deleted; gate diffs route to ladder_suspended with human-accept fallback (main-epoch-14)
- Memory-path diffs (.odo/, wiki/) are rejected at registration time via the same SSOT predicate as the accept-time backstop — fail fast instead of burning panel spend on a permanently unlandable diff, with salvage patches retained in .odo/diffs/ (main-epoch-24)
- wiki/ is the daemon memory pipeline's own content area (distill/curate write it, commitWiki auto-commits): agent diffs carrying wiki hunks are misrouted content and must land through that pipeline, never the diff channel (main-epoch-24)
- A diff touching a protected wiki path is rejected whole at registration — the entire patch is retired including its unrelated Go/GUI hunks; wiki topic corrections must flow through the daemon's own distill/wiki-commit pipeline (main-epoch-42)
- Supply-chain surfaces (package.json/lockfiles, CI workflows) stay hard-blocked from auto-land — a one-line lockfile change is an RCE vector diff review structurally cannot judge; dev-only deps land via separate human commits (bug-fix-epoch-23)
- C0 autonomy classification stays memory-prefix-only; gate-source hits are deliberately not folded into the autonomy ladder (bug-fix-epoch-14)
- Risk classifier hardening: env-dump forms pair with suffix commands, rm variants split-tokenize (rm -r -f, -Rf, multi-space, --recursive --force), and CI workflow paths flow through one SSOT predicate shared by gate and classifier (main-epoch-23)
- Gate paths use case-folded prefix matching with renames checked on both pre-image sides, so a rename out of a protected prefix is still gate-source (bug-fix-epoch-14)
