# Scripts

`bootstrap.sh` installs the pinned Go and Protocol Buffer tools under a local
prefix. `run_devnet.sh` builds binaries, starts one worker and node per
generated config, and waits until every worker socket and RPC endpoint is
ready. `stop_devnet.sh` stops those processes and waits for them to exit.
`smoke_test.sh` creates a three-node run, submits two queries, checks height-2
convergence and decrypts the winning output. `collect_events.py` turns JSONL
ledgers into a compact experiment summary while preserving the raw files.

