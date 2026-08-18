# Testing & Verification Conventions

- Wire-exact receipt pins are the house standard: test stubs capture the real HTTP request body, independently recompute sha16, and compare against the journaled receipt — applied to moa single-shot, retry byte-identity, escalation-to-final-body, tool-loop final round, and no-receipt-on-error (daemon-misc-epoch-2)
- Retry behavior is tested hermetically via a sleepRetry seam that records the backoff table with zero wall time; 21/21 pins pass including 500→200 retry, 429 Retry-After honored-and-capped, network-error exhaustion, and cancellation no-retry (moa-chain-epoch-1)
- The Design-MoA suite grew from 4 to 7 tests, adding TestDesignMoaResponseWireKeys which marshals a Response carrying both field groups to catch wire-level key conflicts that struct assertions cannot (moa-chain-epoch-3)
- A reverse pin was added for truncation: a degraded pass MUST NOT emit the failure marker, alongside the full fail-closed matrix (single-leg degrade / all-legs-fail / consolidator truncation) (moa-chain-epoch-3)
- Playwright e2e grew 65 → 69 → 74 specs across GUI waves; the fixture-isolation lesson is that receipt rows must live in conv 3 because conv 1 placement collided with MessageBubble rendering and broke diff.spec/review-inbox.spec (gui-wave-epoch-1)
- Mid-run e2e failures are root-caused, not dismissed: the completion-flash failure traced to the mock identity bug, and a full-suite wipe traced to dead-shell-cwd breaking vite — environmental, not product (gui-wave-epoch-2)
- GUI verification includes real browser screenshots as evidence (context ring red at ~86%, meter popover, Panel ×3 popover, both stats-strip branches) plus tsc --noEmit clean (gui-wave-epoch-2)
- npx vitest run is pre-existingly broken in the worktree (no vitest config; collects e2e/*.spec.ts causing double-registration) and was declared out of scope but remains open (gui-wave-epoch-1)
