# Housekeeping and Journal Maintenance

- Op3 cleanup (kept after rollback): deleted 6 stale session+prompt pairs while preserving live session 6a7852d9-bd583ceb9585, removed gui/dist and gui/test-results, truncated daemon.log; wiki/ and .odo/ledger.md were already absent (epoch-2)
- journal.sqlite reset was deferred: the live daemon (PID 30215) holds the SQLite WAL and the session writes it in real time, so deletion would corrupt running state (epoch-1)
- journal.sqlite reset remains a manual pending step requiring Odo fully quit: `cd ~/Projects/odo && rm .odo/journal.sqlite*`; bootstrap recreates an empty journal on next launch (epoch-4)
- memory/log.md HEAD marker lagged: log said 6ecbac0 while actual HEAD was ac8bed8+ at the time (epoch-3)
