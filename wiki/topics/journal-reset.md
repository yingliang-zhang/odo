# Journal Database Reset

- journal.sqlite reset deferred until Odo fully quits: the live daemon (PID 30215) holds the SQLite WAL and this session writes it in real time — deleting while running corrupts state (epoch-2)
- Manual procedure: quit Odo, then `cd ~/Projects/odo && rm .odo/journal.sqlite*`; bootstrap recreates an empty journal on next launch (epoch-1)
- Still pending as of the latest epoch note (epoch-7)
