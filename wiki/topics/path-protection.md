# Path Protection & Memory Guard

- Hard blocks are minimal by design: `.odo/`/`wiki/` memory paths plus supply-chain manifests/lockfiles only; `protectedGateFiles` are merely risk annotations fed into panel facts, not blockers (main-epoch-24) (UI-epoch-11)
- Memory-path protection applies to every actor including a human Accept click (`rejectMemoryPaths`, invariant: agents never write memory); diff #38 carrying a `wiki/topics/gui-visibility.md` hunk was permanently unlandable and was rejected for ledger cleanup (main-epoch-24)
- Memory-path diffs are refused at registration time (drainRun guard after ExtractDiff, before InsertDiff) with a transcript advisory naming paths and a salvage patch retained; loop runs classify as `run_tainted` with the refusal detail instead of unsolvable "land it manually" advice (main-epoch-24)
- Wiki content routes only through distill/`commitWiki` — the memory pipeline owns that area and auto-commits it; an agent diff carrying a wiki hunk is misrouted content that can never land (main-epoch-24)
- Supply-chain stays hard-blocked: a one-line lockfile dependency change is an RCE vector that diff review structurally cannot judge (main-epoch-24)
- Doctrine #27 (5ec522b) downgraded gate source/new top-level dirs/net-new assertions from hard blocks to three-valued `autoLandCheck` risk annotation + execution-layer patch_sha16 byte binding; `rejectProtectedPaths`/`rejectExecutorPaths` deleted with zero residue (UI-epoch-11)
- Risk classifiers hardened as one batch: env-dump tokens caught without suffix pairing, split `rm -r -f` via punctuation tokenization, CI workflow paths via the `autoLandSupplyChainPath` SSOT shared by gate and classifier (main-epoch-23)
- Most historical manual gates came from diffs touching `protectedGateFiles` (especially `autoland.go`); the mitigation for review fatigue is batching protected-file changes into larger single diffs, not loosening the surface (UI-epoch-6)
