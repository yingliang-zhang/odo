# DESIGN LOCK — Wave 4: moa_fs_deny replace → merge

Tri-model mini-design consolidated 2026-08-14 (K3/GLM/DSF). GLM's
`replace:`-sentinel proposal and K3's protected credential floor are the
recorded dissent (2/3 against each, comment them in the ADR's
"Alternatives considered" section).

## Locked semantics — ONE helper

`internal/ipc/fstools.go`:

```go
func parseFSDeny(raw string) []string
```

1. Start from `defaultFSDeny` (declared order preserved — baseline always
   wins; fail-closed).
2. Split `raw` on `,`, trim. Bare tokens = additions (appended in file
   order, deduped case-insensitively against everything seen — matching
   check()'s fold). `-`-prefixed tokens = removals (subtract ONE default
   or previously added entry by name, case-insensitive).
3. Contradiction (same name both added and removed) → the name STAYS
   DENIED in either token order (addition survives, toward DENY).
4. `raw` absent, empty, whitespace, or noise-only (`,,,`, ` - `) →
   exactly `defaultFSDeny`. THIS ALSO CLOSES THE LIVE HOLE at
   fstools.go:96-104 (`moa_fs_deny: ,` currently sets entries=nil —
   zero paths denied).
5. Result is never nil. NO `replace:` sentinel. NO protected floor —
   any entry including credentials is removable via `-` (a recorded
   conscious act).
6. `newFSToolExecutor` replaces its parse block with
   `entries := parseFSDeny(adapter.LoadPrefsRaw("moa_fs_deny"))`.
   `defaultFSDeny` contents unchanged (W3 batch stays).

## Header comments

fstools.go:12-16 deny comment → "extends the built-in deny list; a `-`
prefix removes one non…(any) entry, noise/empty keeps the built-ins" and
describeScope's "Excluded:" line comment stays accurate.

## ADR

`docs/adr/0004-moa-fs-deny-merge-semantics.md` EXISTS (K3's design-leg
draft, Status: Proposed) — REVISE it to this lock: semantics above;
Alternatives considered = pure-union / replace-sentinel (GLM, rejected:
can silently skip future default batches and re-admit credentials by one
magic word) / protected floor (K3, rejected: converts an operator's
conscious `-` into a silent policy override — a floor revision deserves
its own ADR if ever needed); Migration = silent safe-direction flip, no
prefs rewrite (describeScope surfaces the effective list); GUI follow-up
(read-only effective-list display) documented as future, not this wave.
Status → Accepted (2026-08-14). Keep the ADR ~1 page.

## Tests (9, names fixed — fstools_test.go/server_test.go)

1. `TestParseFSDenyDefaultsOnEmpty`
2. `TestParseFSDenyUnion` (restated default dedupes)
3. `TestParseFSDenyRemoval` (removes only the named; `-nope` no-op)
4. `TestParseFSDenyRemovalCaseInsensitive`
5. `TestParseFSDenyContradictionDenies` (same token added+removed →
   stays denied)
6. `TestParseFSDenyNoiseTokens` (`,,,`, `" - "` → full defaults — the
   live-nil hole regression pin)
7. `TestNewFSToolExecutorDenyMerge` (prefs `tmp` → denies ~/.ssh AND
   ~/tmp — headline fail-open regression, fails on old code)
8. `TestNewFSToolExecutorDenyReset` (absent vs empty both → defaults)
9. `TestParseFSDenyRemovalAnyEntry` (`-.ssh` removes .ssh — pins the
   locked NO-floor semantics so a later floor ADR must change this test)

Verify:
```
cd ~/Projects/odo
go build ./... && go vet ./internal/...
go test ./internal/ipc/ -run 'ParseFSDeny|FSToolExecutor|FSDeny|FS' -count=1
go test ./internal/ipc/ -count=1
```
Both green. No git add/commit; touch only fstools.go, its tests,
server_test.go if needed, docs/adr/0004 + docs/adr/README.md (index line
already exists from the design leg — verify it). Other uncommitted files
untouched; contradiction → STOP and report. Report files changed, tests,
results.
