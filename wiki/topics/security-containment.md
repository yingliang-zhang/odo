# Security Containment & Path Guards

- Unified guard: a three-arg predicate anchored at the canonical projectRoot rejects symlinked root nodes (.odo, wiki, wiki/topics) and contains resolutions inside the project on both read and write sides; escapes degrade to absent/vanished semantics rather than new error faces (main-epoch-34)
- Containment scope is project-side committable surfaces only: global ~/.odo files are outside the threat model (guarding them would break legitimate dotfile symlinks); diff PathOnDisk and vision attachments were deliberately left unguarded (main-epoch-30, main-epoch-34)
- Skill scope resolves its whole directory chain through guardedBase; handleDeleteSkill gained the guard handleUpdateSkill already had — a symlinked .odo/skills otherwise degraded all project skills to absent (main-epoch-38)
- /preview redirect bypass closed two ways: per-hop redirect validation with final-URL capture, then an in-process loopback-only filtering proxy injected into the Playwright child that denies off-loopback requests before dial; JS/meta-refresh redirects are documented as an accepted v1 boundary (main-epoch-30, main-epoch-32)
- /preview's Playwright dependency is pinned to the lockfile version 1.62.1 (never playwright@^1) with an explicit setup phase and offline verification (main-epoch-34)
- Loop artifacts fail closed: loopArtifactBody validates containment and compares <field>_sha16 at both read sites, so tampered/escaped/symlinked findings and design_lock bodies are rejected (main-epoch-38)
- Reads are capped during read, not after: the loop task file gained an os.Stat pre-check plus anchored readWithinDir, and readWithinDir was replaced by a capped reader enforcing the limit before allocation (main-epoch-38, main-epoch-42)
- handleReadFile streams large files via io.LimitReader instead of whole-file allocation, contradicting its old comment (main-epoch-28)
- Skill scan enforces a 64KB per-file limit and skips (break→continue) oversized files so a large skill cannot block smaller relevant ones (main-epoch-38)
- Accept commits are byte-exact: alreadyLanded pre-commit builds the post-image via a temp index and compares worktree hash-object per file, so stray edits can't ride 'odo: accept diff' commits; staged-only divergence is refused via IndexEditsBeyondHEAD before adjudication (main-epoch-30, main-epoch-32)
- writeTopicPages stale-file cleanup (os.Remove) was pre-guarded — it previously bypassed checks before the guard ran (main-epoch-34)
