# Experiment workflow

The devnet workflow uses separate processes and separate data directories on
one host. The same commands work when the node and worker binaries are copied
to separate hosts and the TOML listen and seed addresses are changed.

## Prepare a run

```bash
go run ./cmd/pomlctl init-devnet --dir run/devnet --nodes 3
```

Archive `genesis.json`, `manifest.json`, the node TOML files, the worker
profile, the binary revision and the machine manifest before changing a run.
The generator chooses a large base target for short local smoke tests. Use a
smaller target in an experiment whose purpose is measuring ticket throughput.

## Start and stop

```bash
./scripts/run_devnet.sh run/devnet
./scripts/stop_devnet.sh run/devnet
```

The start script launches one worker and one node for every generated config.
It writes stdout and PID files below the run directory. The stop script sends
TERM and waits for each process. Event JSONL files remain available after the
processes exit.

## Submit workload

```bash
go run ./cmd/pomlctl submit-query \
  --node 127.0.0.1:19100 \
  --user run/devnet/user.json \
  --input "sample request" \
  --expiry 100 \
  --fee 10
```

The CLI signs the query locally. The node receives only the signed public
transaction and relays it. The user private key remains in `user.json`.

## Collect metrics

```bash
python3 scripts/collect_events.py run/devnet --output run/devnet/summary.json
```

The collector reports event counts per node, block heights and hashes,
candidate hits, accepted blocks, forks, rejections, peer handshakes and
worker timing fields. The raw ledgers remain the source for later E1/E2/E3/E4
analysis.

## Restart check

Stop one node after it has accepted a block, start it again with its same TOML
file and compare `get-tip` with the other nodes. Pebble restores the tip,
historical state, mempool and miner attempt counter. A fresh node should use a
new data directory and synchronize from a configured seed.

## Multi-host runs

Use independent disks and keys on every host. Set `advertise` to an address
reachable by peers, put at least one reachable seed in each TOML file and keep
the same genesis file on every host. Record link interruptions, host clock
offsets, CPU model, Go version, binary revision and worker profile in the run
manifest. The experiment runner should submit workload and collect logs through
public interfaces only.

