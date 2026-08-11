# Outstanding Work and Anomalies

- M7 GUI webview E2E (cua-driver) remains outstanding since M7 closed (epoch-2)
- steering.txt write path in omp.go is dead code; Adapter interface not cleaned (A2 brief RC8) (epoch-2)
- Op2 install-to-/Applications ambiguity: /Applications/Odo.app appeared built 18:01 and running 18:04 though Op2 was formally deferred; origin unconfirmed in-transcript (epoch-4)
- Earlier anomalies still uninvestigated: agent_error exit 127 (omp: command not found), one agent_text containing raw session JSON, and a 401 auth error (epoch-6)
- Distill provenance back-link validation (epoch-note conclusions citing journal seq ranges + mechanical check) still undecided/unimplemented (epoch-7). The conditional auto-curate half SHIPPED 2026-08-10 (ed769e8: ≥4 notes OR 7d + citation-liveness pre-write) — epoch-7's "unimplemented" note on that half is stale
- M16 auto-land ACTIVATED 2026-08-11: main 1fb31d6 (diffs #8–#13) pushed, `~/.odo/prefs.md` now has `auto_apply: main`, daemon binary rebuilt from HEAD and SIGTERM-restarted (GUI send-path auto-respawns from repo binary). `.odo-verify` runs build+vet+test. Journal sqlite* reset still NOT done — needs full Odo.app quit; optional, window stays open (epoch-8)
