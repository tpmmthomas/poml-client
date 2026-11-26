#!/usr/bin/env python3
"""Summarize the public JSONL event ledgers from one devnet run."""

from __future__ import annotations

import argparse
import collections
import json
from pathlib import Path


def collect(run_dir: Path) -> dict:
    result = {"run_dir": str(run_dir), "nodes": {}}
    for path in sorted(run_dir.glob("run/node-*/events.jsonl")):
        counts = collections.Counter()
        statuses = collections.Counter()
        blocks = set()
        heights = []
        worker_ns = []
        for line in path.read_text().splitlines():
            event = json.loads(line)
            counts[event.get("event_type", "unknown")] += 1
            statuses[event.get("status", "unknown")] += 1
            if event.get("block_hash"):
                blocks.add(event["block_hash"])
            if "height" in event:
                heights.append(event["height"])
            if event.get("inference_ns"):
                worker_ns.append(event["inference_ns"])
        result["nodes"][path.parent.name] = {
            "event_counts": dict(sorted(counts.items())),
            "status_counts": dict(sorted(statuses.items())),
            "block_hashes_seen": sorted(blocks),
            "max_height_observed": max(heights, default=0),
            "inference_ns_samples": len(worker_ns),
            "inference_ns_total": sum(worker_ns),
        }
    return result


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("run_dir", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    summary = collect(args.run_dir)
    encoded = json.dumps(summary, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.write_text(encoded)
    else:
        print(encoded, end="")


if __name__ == "__main__":
    main()
