#!/usr/bin/env bash
set -euo pipefail

run_dir="${1:-run/devnet}"
mkdir -p "$run_dir/bin"
go build -o "$run_dir/bin/pomld" ./cmd/pomld
go build -o "$run_dir/bin/pomlworker" ./cmd/pomlworker
go build -o "$run_dir/bin/pomlctl" ./cmd/pomlctl

certificate_key=$(awk -F' = ' '$1 == "certificate_private_key_hex" {gsub(/"/, "", $2); print $2}' "$run_dir/node-0.toml")
if [ -z "$certificate_key" ]; then
  echo "node-0.toml has no certificate_private_key_hex" >&2
  exit 1
fi

for config in "$run_dir"/node-*.toml; do
  [ -e "$config" ] || continue
  node_index=$(basename "$config" | sed -E 's/node-([0-9]+)\.toml/\1/')
  socket="$run_dir/run/worker-${node_index}.sock"
  nohup "$run_dir/bin/pomlworker" --socket "$socket" \
    --certificate-private-key-hex "$certificate_key" \
    >"$run_dir/worker-${node_index}.log" 2>&1 </dev/null &
  echo $! >"$run_dir/worker-${node_index}.pid"
done

for config in "$run_dir"/node-*.toml; do
  [ -e "$config" ] || continue
  node_index=$(basename "$config" | sed -E 's/node-([0-9]+)\.toml/\1/')
  nohup "$run_dir/bin/pomld" --config "$config" \
    >"$run_dir/node-${node_index}.stdout.log" 2>&1 </dev/null &
  echo $! >"$run_dir/node-${node_index}.pid"
done

# Do not return until every worker socket and operator RPC can answer. This
# keeps callers from racing daemon startup and also turns an early child
# failure into a useful error at the launcher boundary.
for config in "$run_dir"/node-*.toml; do
  [ -e "$config" ] || continue
  node_index=$(basename "$config" | sed -E 's/node-([0-9]+)\.toml/\1/')
  worker_socket=$(awk -F' = ' '$1 == "worker_socket" {gsub(/"/, "", $2); print $2}' "$config")
  case "$worker_socket" in
    /*) ;;
    *) worker_socket="$run_dir/$worker_socket" ;;
  esac
  rpc_address=$(awk -F' = ' '$1 == "rpc" {gsub(/"/, "", $2); print $2}' "$config")
  rpc_token=$(awk -F' = ' '$1 == "rpc_token" {gsub(/"/, "", $2); print $2}' "$config")
  worker_ready=false
  rpc_ready=false
  for attempt in $(seq 1 80); do
    if [ -S "$worker_socket" ]; then
      worker_ready=true
    fi
    if "$run_dir/bin/pomlctl" get-tip --node "$rpc_address" --token "$rpc_token" >/dev/null 2>&1; then
      rpc_ready=true
    fi
    if [ "$worker_ready" = true ] && [ "$rpc_ready" = true ]; then
      break
    fi
    sleep 0.1
  done
  if [ "$worker_ready" != true ] || [ "$rpc_ready" != true ]; then
    echo "node-$node_index did not become ready" >&2
    tail -n 40 "$run_dir/node-${node_index}.stdout.log" >&2 || true
    tail -n 40 "$run_dir/worker-${node_index}.log" >&2 || true
    exit 1
  fi
done

echo "Started $(printf '%s\n' "$run_dir"/node-*.toml | wc -l) node processes in $run_dir"
