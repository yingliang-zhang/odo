# GUI Chrome Defects & Wave Features

- Transparent popovers root cause: twMerge treats any bg-* class as the background-color group, so the inert e2e marker class bg-runs-menu discarded PopoverContent's real background; fix renamed the marker on all 5 StatusBar popovers and left a comment warning against bg-prefixed marker classes (UI-epoch-7)
- Status-bar icon stacked above text because Tailwind preflight sets svg display:block inside a non-flex span; fix adds inline-flex items-center following the STATUS_BADGE convention (same root cause also hit the Copied! check icon) (UI-epoch-3)
- Right sidebar close X removed in favor of a TopBar toggle mirroring the left-sidebar pattern (⌘J shortcut unchanged) (UI-epoch-7)
- Resize grip was unusable because z-0 painted it under the header/body; fix raised to z-20 and extended the hit area 4px right while preserving the px-2 header contract (main-epoch-32)
- Wave B GUI-only features: context-pressure meter as SVG ring with 50/80 thresholds and click-through composition popover; per-turn stats strip deriving honestly (wall time + in/out bytes, tok/s only when real counts exist, never fabricated); read-only MoA panel chip (model changes stay in SettingsPanel) (gui-wave-epoch-2)
- GUI usage/token-rate display is future-proofed: reverse-scans journaled total_prompt_bytes closures and defensively reads input/output_tokens so it auto-upgrades once the daemon journals OMP usage at message_end (daemon wave still required) (gui-wave-epoch-2)
- Advisory-slash robustness: 15 poll-dependent assertions tagged POLL=12s while RPC-local surfaces were deliberately left at 5s because tagging them would mask mechanism; timeout relaxation beyond the existing 12s REFRESH convention was refused to avoid a second convention (main-epoch-30)
