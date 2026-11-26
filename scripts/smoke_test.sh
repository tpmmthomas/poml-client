#!/usr/bin/env bash
set -euo pipefail

run_dir="${1:-$(mktemp -d /tmp/poml-stage1-smoke.XXXXXX)}"
cleanup() {
  ./scripts/stop_devnet.sh "$run_dir" >/dev/null 2>&1 || true
}
trap cleanup EXIT

go run ./cmd/pomlctl init-devnet --dir "$run_dir" --nodes 3 >/dev/null
./scripts/run_devnet.sh "$run_dir" >/dev/null

# Process startup is deliberately asynchronous.  Wait for every operator RPC
# before sending test traffic so this smoke test is stable on a busy host.
for port in 19100 19101 19102; do
  ready=false
  for attempt in $(seq 1 50); do
    if "$run_dir/bin/pomlctl" get-tip --node "127.0.0.1:$port" >/dev/null 2>&1; then
      ready=true
      break
    fi
    sleep 0.1
  done
  if [ "$ready" != true ]; then
    echo "RPC did not become ready on 127.0.0.1:$port" >&2
    exit 1
  fi
done

"$run_dir/bin/pomlctl" submit-query \
  --node 127.0.0.1:19100 --user "$run_dir/user.json" \
  --input smoke-one --nonce 0 --expiry 100 --fee 10 >/dev/null
sleep 4

# Leave one miner active so a longer branch demonstrates reorganization and
# convergence after the first equal-height fork.
"$run_dir/bin/pomlctl" stop-mining --node 127.0.0.1:19101 >/dev/null
"$run_dir/bin/pomlctl" stop-mining --node 127.0.0.1:19102 >/dev/null
"$run_dir/bin/pomlctl" submit-query \
  --node 127.0.0.1:19100 --user "$run_dir/user.json" \
  --input smoke-two --nonce 1 --expiry 100 --fee 10 >/dev/null

tip_hash=""
tip_results=""
for attempt in $(seq 1 80); do
  tip_results=$(for port in 19100 19101 19102; do
    "$run_dir/bin/pomlctl" get-tip --node "127.0.0.1:$port"
  done)
  if tip_hash=$(printf '%s\n' "$tip_results" | python3 -c '
import json, sys
tips = [json.loads(line) for line in sys.stdin if line.strip()]
if len(tips) != 3 or len({tip["hash"] for tip in tips}) != 1 or any(tip["height"] < 2 for tip in tips):
    sys.exit(1)
print(tips[0]["hash"])

'); then
	break
	fi
	tip_hash=""
	sleep 0.1
done
if [ -z "$tip_hash" ]; then
	echo "nodes did not converge to height 2: $tip_results" >&2
  exit 1
fi

"$run_dir/bin/pomlctl" decrypt --node 127.0.0.1:19100 \
	--user "$run_dir/user.json" --hash "$tip_hash" >/dev/null
python3 scripts/collect_events.py "$run_dir" --output "$run_dir/summary.json"
echo "smoke test passed with canonical tip $tip_hash"
