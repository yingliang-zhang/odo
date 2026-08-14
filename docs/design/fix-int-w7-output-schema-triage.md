# Wave 7: backlog triage — OMP output-schema flag check

## Result: DEFERRED

`omp --help` lists: `--mode=<value>` (text/json/rpc/rpc-ui), `--print-thoughts`,
`--hide-thinking`, `--no-lsp`. **No `--output-schema` or equivalent
structured-output flag exists.**

The 2026-08-13 harness audit item #5 (P1, "Constrain MoA legs' final
answers to JSON schema") is therefore deferred to the audit's fallback
path: **schema-in-prompt + strict validator** (parse the leg's text
output against a JSON schema embedded in the prompt; reject malformed
legs as infra-failures). This is a future implementation wave — no code
change this wave.

## Evidence

```
$ omp --help | grep -i "schema\|output\|json\|format"
--mode=<value>        Output mode: text (default), json, rpc, or rpc-ui
--no-lsp              Disable LSP tools, formatting, and diagnostics
--hide-thinking       Hide thinking blocks in TUI output
--print-thoughts      Include thinking blocks in print mode text output
```
