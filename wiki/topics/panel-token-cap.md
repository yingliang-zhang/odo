# Panel Thinking-Model Token Cap

- /panel truncation root cause: defaultMaxTok = 4096 in internal/moa/client.go; thinking models (kimi-k3, deepseek-v4-flash) burn output budget on reasoning traces → stop_reason=max_tokens (epoch-5)
- Raised defaultMaxTok 4096→16384 with comment on thinking-model budget; accepted via GUI as commit 1de583c on main, unpushed (epoch-7)
- Verified with grounded 3-model fan-out at 16384: all returned end_turn with 7325/8076/8550 output tokens — all above 4096, proving the old cap structurally insufficient (epoch-6)
- Fix not effective on live daemon: subsequent /panel runs still hit 4096 max_tokens because the daemon runs the old binary; needs rebuild + restart (cannot kill daemon mid-session) (epoch-7)
- /panel questions must be grounded by pasting code facts into the prompt — panel models have no tool/file access; ungrounded first round produced only generic frameworks from glm-5.2 (epoch-7)
