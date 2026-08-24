# Risk Classification & Supply-Chain Gate

- Severity taxonomy: credential_probe = critical; data_exfil and destructive = high; security_weakening = medium; supply_chain = low; `"none"` means rated-clean while an absent class means unrated (rendered dashed-neutral, never masquerading as clean) (gui-wave-epoch-1)
- Classifier hardening pins: `riskEnvDumpTokens` catches `os.Environ(` dump forms as credential_probe without suffix pairing; `riskRmSplit` FieldsFunc punctuation tokenization catches `rm -r -f`, `rm -Rf`, multi-space, and `--recursive --force` (main-epoch-23)
- `autoLandSupplyChainPath` is the SSOT predicate (`.github/workflows/` + `.gitlab-ci.yml`) shared by the auto-land gate and the risk classifier (main-epoch-23)
- Supply-chain manifests/lockfiles stay hard-blocked: a one-line lockfile dependency change is an RCE vector that diff review structurally cannot judge (main-epoch-24)
