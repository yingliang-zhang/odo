# Test HOME Isolation

- TestVisibleLoopAcceptRejectRestore failure root-caused as pre-existing bug, not the moa change: missing t.Setenv("HOME", t.TempDir()) let readUserMemory() inject the real ~/.odo/user.md into the stubbed prompt, breaking the hello.txt == msg1 assertion; passes with isolated HOME (epoch-6)
- Audit found 15 run-loop ipc tests lacking HOME isolation (server_test.go ×8, concurrent_test.go ×6, streaming_test.go ×1); t.Run scenarios need per-subtest Setenv (epoch-5)
- Isolation edits landed in the 7559f7d/ac8bed8 vicinity per latest note (epoch-7)
