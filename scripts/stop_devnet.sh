#!/usr/bin/env bash
set -euo pipefail

run_dir="${1:-run/devnet}"
declare -a pids=()
for pid_file in "$run_dir"/*.pid; do
  [ -e "$pid_file" ] || continue
  pid=$(cat "$pid_file")
  pids+=("$pid")
  kill "$pid" 2>/dev/null || true
done
for attempt in $(seq 1 50); do
  alive=false
  for pid in "${pids[@]}"; do
    if kill -0 "$pid" 2>/dev/null; then
      alive=true
      break
    fi
  done
  [ "$alive" = false ] && break
  sleep 0.1
done
for pid in "${pids[@]}"; do
  kill -KILL "$pid" 2>/dev/null || true
done
echo "Stopped processes recorded in $run_dir"
