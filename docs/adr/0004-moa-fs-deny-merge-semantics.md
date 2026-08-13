# ADR-0004: moa_fs_deny Merge Semantics — Fail-Closed Union with `-` Removal

## Status

Accepted — 2026-08-14 (W4 tri-model mini-design lock; implemented in the
same wave).

## Context

`newFSToolExecutor` (internal/ipc/fstools.go) treated a present
`moa_fs_deny:` prefs line as a FULL REPLACEMENT of `defaultFSDeny`:

```go
entries := defaultFSDeny
if raw := adapter.LoadPrefsRaw("moa_fs_deny"); raw != "" {
    entries = nil                              // defaults dropped entirely
    for _, d := range strings.Split(raw, ",") { ... }
}
```

Two fail-open holes on a security contract whose contents ship to a
third-party model gateway:

1. `moa_fs_deny: tmp` silently drops .ssh/.aws/.gnupg/.netrc/… plus the
   whole 2026-08 SEC audit batch (.claude, trustdb.gpg, …).
2. `moa_fs_deny: ,` — non-empty value, zero tokens — builds a nil
   `entries`, i.e. NOTHING is denied. The sharpest hole: there was no way
   to distinguish "user wants one extra exclusion" from "user wants an
   empty list", and the parser always chose the dangerous reading.

The legitimate need behind replace-semantics is real: deny entries resolve
root-relative (`filepath.Join(root, d)`), so with `moa_fs_root:
~/Projects/odo` the defaults deny that repo's own `Makefile`, `CLAUDE.md`,
`node_modules`, `__pycache__`, `swap`. Pure union with no removal
mechanism leaves such an operator no escape hatch short of a code edit.

## Decision

prefs EXTENDS defaults; a `-name` token removes one entry — any entry,
credentials included. All semantics live in one pure helper:

```go
func parseFSDeny(raw string) []string
```

1. Start from `defaultFSDeny`, declared order preserved — the baseline
   always applies (fail-closed). Contents unchanged; the 2026-08 SEC
   audit batch stays.
2. Split `raw` on `,`, trim. Bare tokens are additions, appended in file
   order and deduped case-insensitively against everything seen (matching
   check()'s fold). `-`-prefixed tokens are removals: subtract one default
   or previously added entry by name, case-insensitive.
3. A name both added and removed stays denied — contradiction resolves
   toward DENY, independent of token order.
4. `raw` absent, empty, whitespace, or noise-only (`,,,`, `" - "`) yields
   exactly `defaultFSDeny`. This also closes hole 2: there is no syntax
   for an empty list.
5. The result is never nil. NO `replace:` sentinel. NO protected floor:
   any entry, including the credential set, is removable via an explicit
   `-` token — a recorded conscious operator act.
6. `newFSToolExecutor` is now
   `entries := parseFSDeny(adapter.LoadPrefsRaw("moa_fs_deny"))`; the
   root/tilde/absolute resolution loop is untouched.

The effective list stays observable: `describeScope()` already flattens it
into the /panel system prompt ("Excluded: …"), so provenance is positional
— anything after the last default entry came from prefs.

### Empty / clear semantics

- Absent key or `moa_fs_deny:` (empty value) → defaults, exactly. This is
  also the GUI "clear the field" path: factory reset = clear/delete the
  line.
- Noise-only values → defaults (hole 2 regression-pinned by test).

## Alternatives considered

### Pure union, no removal (fail-closed, maximal)

Rejected as the sole mechanism. Provably closes both holes, but the
root-relative collision case (root = a Go/JS project → Makefile,
CLAUDE.md, node_modules, __pycache__, swap all denied; this very repo hits
it) has no operator escape hatch, and an unusable safe default pressures
the next change toward re-opening the list. Kept as the behavior for
everything except the explicit `-` token.

### Opt-in `replace:` sentinel for full replacement — GLM dissent (2/3 against)

Rejected. A magic first token re-admits total shrinkage — including the
credential set — behind one word: it can silently skip every future
daemon-shipped default batch (the 2026-08 SEC batch pattern) and re-admit
credentials the operator never named. It also leaks parser magic into the
GUI free-text field. The `-` token expresses shrinkage granularly and
keeps new defaults flowing.

### Protected credential floor — K3 dissent (2/3 against)

Rejected for this wave. A non-removable core (.ssh/.aws/.gnupg/…)
converts an operator's conscious `-` token into a silent policy override:
the prefs file says remove, the daemon quietly doesn't. The lock instead
keeps one predictable rule — explicit operator intent wins — and pins it
with `TestParseFSDenyRemovalAnyEntry`. If a floor is ever warranted, that
revision deserves its own ADR and must change that test, not ride in on a
parser tweak.

## Migration

Silent safe-direction flip, no prefs rewrite. Old replace-style values
keep working: custom entries still apply, defaults re-appear alongside.
Nothing that was denied becomes readable; the worst outcome is a
newly-denied path whose error names `moa_fs_deny` and whose cause is
visible in the /panel scope line. No startup transform is attempted
because old intent (restating defaults vs deliberately omitting one) is
unrecoverable from the value — any rewrite is a guess, and a guess that
mutates the very config file this security contract reads (M12
migrateAutoCuratePref precedent notwithstanding).

## Consequences

- Accidental fail-open modes (full replace, noise-only values) are gone by
  construction, not by validation.
- Operators regain granular control: `-Makefile, -CLAUDE.md` un-denies a
  project root's own files without forking the default list, and
  daemon-shipped default additions keep applying.
- Contradictory prefs resolve toward DENY in both token orders — the
  surprising direction is the safe direction.
- GUI follow-up (future wave, not this one): a read-only effective-list
  display in Settings, plus field help text ("extends the built-in deny
  list; prefix an entry with `-` to remove it"). No odo-side blocker; the
  effective list is already surfaced via describeScope.
