# Composer, IME & Advisory Slash

- Advisory slash (/panel, /vision, /preview) runs synchronously inside the daemon `send_message` RPC (multi-minute); GUI fix: detached from composer await — box clears/unlocks immediately, background promise drives spinner, draft restored only on instant rejection (bug-fix-epoch-3)
- Accepted semantic change: users can now send normal messages during a panel consult — judged safe (panel read-only, slashing slot only rejects distill, answers carry `panel` flag excluded from fold/replay) (bug-fix-epoch-3)
- Late advisory rejection must not clobber a newer draft: restore only if textarea + attachments untouched; else keep user content, show error banner only (bug-fix-epoch-4)
- Daemon routes slash commands before steer/park branches → advisories never consume a parked one-shot arm; GUI uses `clearComposer({keepPark:true})` (bug-fix-epoch-4)
- `panelThinking` boolean → counter so concurrent advisories share the spinner; last one out turns it off (bug-fix-epoch-4)
- Whitespace parity: daemon matches only literal space prefix; `/panel⏎`, tab, `/ panel` fall through on both sides — pinned by comments + 17 daemon-mirror unit cases, no behavior change (bug-fix-epoch-4)
- IME stuck-blue root cause: WKWebView Enter confirming a candidate can arrive without isComposing/keyCode 229 → submit fires mid-composition → `compositionend` never fires → composingRef stuck true blocks all future writes; fix: synchronously clear value+flag on send (UI-epoch-4)
- Composer is uncontrolled (`defaultValue`) for React 19 composition semantics — 350ms poll re-renders were writing stale `draft` back and destroying Chinese IME composition during steering; programmatic writes via layout effect gated on `!composing` (UI-epoch-2)
- WebKit-only bugs need WebKit harnesses: Chromium Playwright missed two; use Playwright-WebKit + native WKWebView (swiftc) harness; blue-visual verified only at DOM-contract level (pre-fix red / post-fix green) (UI-epoch-2, UI-epoch-4)
