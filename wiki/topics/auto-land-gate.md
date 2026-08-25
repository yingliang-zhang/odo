# Auto-Land Gate Doctrine

- Hard blocks after the 2026-08-20 narrowing are only memory paths (.odo/, wiki/) and supply-chain manifests/lockfiles; gate-source files (autoland/autonomy/risk/contradiction, settle) are panel risk annotations, not blockers (main-epoch-24) (UI-epoch-11)
- autoLandCheck is three-valued, and gate-source diffs require unanimous panel attestation since 2026-08-22; manual Accept legitimately bypasses attestation for gate-source diffs per doctrine (main-epoch-41) (main-epoch-22) (UI-epoch-11)
- panelVerdictAttestsDiff binds the panel verdict to exact landing bytes via patch_sha16; regenerating a patch pre-accept is safe only when the diff never reached panel (no attestation binding broken) (UI-epoch-11) (main-epoch-22)
- Supply chain stays hard-blocked: a one-line lockfile dependency change is an RCE vector diff review structurally cannot judge; autoLandSupplyChainPath is the SSOT predicate covering .github/workflows/ and .gitlab-ci.yml (main-epoch-24) (main-epoch-23)
- Risk classifier catches credential probes via punctuation-tolerant tokenization: os.Environ( dump forms hit credential_probe, and split flags (rm -r -f, rm -Rf, --recursive --force) are caught by FieldsFunc tokenization (main-epoch-23)
- Memory-path diffs are refused at registration time in drainRun (SSOT rejectMemoryPaths) with a transcript advisory pointing to the distill/wiki-commit route plus a salvage patch kept in .odo/diffs/; loop runs record run_tainted instead of the previously unsolvable land-it-manually advice (main-epoch-24)
- wiki/ is the daemon memory pipeline's own content area: distill/curate write it directly and auto-commit via commitWiki, so an agent diff carrying a wiki hunk is misrouted content that can never land through the diff channel (main-epoch-24)
- Review fatigue from protected-path blocks is mitigated by batching gate-source changes into larger single diffs, never by loosening the protection surface (UI-epoch-6)
- The auto-revise repair chain is permanently closed above the 64KB prompt cap ('no silent truncation, ever', pinned by settle_test.go); oversized diffs route to manual fix rounds or manual Accept (main-epoch-33) (UI-epoch-5)
