# MoA Client Resilience & Request Receipts

- R-W1 added bounded retry to the moa client: max 3 attempts, 200ms × 2ⁿ backoff with ±50% jitter, Retry-After honored and capped at 30s; 429/≥5xx/network/timeout retry, 4xx and caller cancellation never retry (moa-chain-epoch-1)
- Typed errors Error{Status, Class, Message, RetryAfter} with five Class constants replaced the old error string; repo-wide grep confirmed zero callers depended on the old format (moa-chain-epoch-1)
- Result ledger fields InputTokens/StopReason/WallSeconds/TokPerSec are purely additive; token fields record the final request while wall time covers the whole logical call including retries and escalations (moa-chain-epoch-1)
- R-W1.5 stamps RequestSHA16/RequestBytes at the post() marshal point — the only location that sees actual wire bytes; error returns carry no receipt per the patch_sha16 absence precedent (daemon-misc-epoch-1)
- Receipt semantics follow the final-request convention: a byte-identical retry chain gets one receipt, while budget escalation rebuilds the body so the receipt points to the final request with superseded bodies visible in the Escalations ledger (daemon-misc-epoch-2)
- Load-bearing premise correction: R-W1 (commit e2f8b61) had NOT added RequestSHA16/RequestBytes as the task claimed — git show plus repo grep disproved it, so client-side wiring was built in full rather than journaled through (daemon-misc-epoch-2)
- The sha16 helper is duplicated between moa and ipc because moa cannot import ipc (import cycle), kept consistent via a 'convention is the contract' comment and identical sha256-prefix scheme (daemon-misc-epoch-2)
- Adding the two receipt fields to ReviewResult covers all three journal sites (manual moa_review, auto-land, skill gate) because each serializes the struct directly; PanelResult got the same fields with infra/error legs omitted by convention (daemon-misc-epoch-2)
- All receipt journaling uses additive JSON keys with omitempty, making it ADR-0002 immune with no new event types and no schema migration (daemon-misc-epoch-1)
