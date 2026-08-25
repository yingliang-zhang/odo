# Path & Symlink Containment

- Canonical projectRoot is the trust anchor: the three-arg guard resolves dir and rejects symlinks at the root nodes themselves (.odo, wiki, wiki/topics); the write side carries the same guard as reads (main-epoch-34)
- Containment scope: project-side paths and wiki/ (git-committable surface) are constrained; global ~/.odo files and user-supplied explicit paths (vision attachments, PathOnDisk) are deliberately outside the threat model (main-epoch-30) (main-epoch-34)
- Containment violations degrade to absent/vanished semantics, never new error faces (main-epoch-30)
- Skill resolution runs through guardedBase: a symlinked .odo or .odo/skills degrades the whole project skill scope to absent (fail-closed); handleDeleteSkill gained the same guard handleUpdateSkill already had; scans enforce a 64KB per-file limit with oversized files skipped (main-epoch-38)
- generateAgentsMD reads project memory/pins via readWithinDir (escape means absent) and refuses symlinked daemon-owned files at write time via Lstat (main-epoch-32)
- Loop artifacts are tamper-checked: readLoopTaskFile does an os.Stat size pre-check plus readWithinDir anchored at resolvedRoot; loopArtifactBody verifies guarded containment plus <field>_sha16 comparison, failing closed at both read sites (main-epoch-38)
- /preview redirect handling: per-hop validation with final_url audit capture (documented v1 boundary: JS/meta-refresh redirects unblocked), later hardened to an in-process loopback-only filtering proxy environment-injected into the Playwright child (main-epoch-30) (main-epoch-32)
