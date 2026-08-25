# Composer, IME & Advisory Commands

- Advisory slash commands (`/panel` etc.) run synchronously inside the daemon RPC for minutes; the GUI detaches advisory submits from the composer await — the box clears and unlocks instantly, restoring the draft only if the daemon rejects immediately at busy gates (bug-fix-epoch-3)
- Late advisory rejection must not clobber a newer draft: restore is conditional on the composer being untouched (textarea value + attachments ref); otherwise only the error banner shows (bug-fix-epoch-4)
- Advisory submits preserve the one-shot park arm — the daemon routes slash commands before the steer/park branches and the GUI uses `clearComposer({keepPark: true})` (bug-fix-epoch-4)
- `panelThinking` is a counter, not a boolean, so concurrent advisories share the spinner correctly with last-one-out semantics (bug-fix-epoch-4)
- Slash whitespace parity (`/panel⏎`, tab, `/ panel` all fall through on daemon and GUI) is verified-correct behavior pinned by 17 daemon-mirror unit cases, not a behavior change (bug-fix-epoch-4)
- IME blue-box root cause: on WKWebView the Enter confirming an IME candidate can arrive without isComposing, firing submit mid-composition; `compositionend` never arrives and a stuck `composingRef` refuses all future writes — fixed by synchronously clearing `ta.value` and `composingRef=false` on successful send, converging all send paths (UI-epoch-4)
- The composer is uncontrolled for React 19 composition semantics: `defaultValue` plus layout-effect programmatic writes gated on `!composing` — React 19 ignores input events during composition, so controlled-value rerenders from poll/heartbeat destroyed Chinese IME text (UI-epoch-2)
- Composer mount-height bug: WebKit counts wrapped placeholder in scrollHeight while Chromium does not; the placeholder is cleared during measurement (same-frame restore) with a width-gated ResizeObserver re-size (UI-epoch-2)
- WebKit-only bugs need WebKit harnesses — Playwright default Chromium missed two bugs, so verification added Playwright-WebKit plus a native WKWebView (swiftc) harness (UI-epoch-2)
