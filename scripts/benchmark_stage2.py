#!/usr/bin/env python3
"""Benchmark one Stage 2 model worker without exceeding the host budget."""

from __future__ import annotations

import argparse
import importlib.util
import json
import os
import statistics
import time
from pathlib import Path

import psutil


ROOT = Path(__file__).resolve().parents[1]
MODULE_SPEC = importlib.util.spec_from_file_location("poml_model_worker", ROOT / "stage2" / "model_worker.py")
assert MODULE_SPEC and MODULE_SPEC.loader
worker_module = importlib.util.module_from_spec(MODULE_SPEC)
MODULE_SPEC.loader.exec_module(worker_module)
from api.proto import poml_pb2 as pb


def percentile(values: list[float], fraction: float) -> float:
    ordered = sorted(values)
    index = min(len(ordered) - 1, int(round((len(ordered) - 1) * fraction)))
    return ordered[index]


def request_for(profile: dict, input_bytes: bytes):
    return pb.WorkerRequest(
        version=1,
        profile_id=profile["id"],
        profile_digest=worker_module.profile_digest(profile),
        model_digest=bytes.fromhex(profile["model_digest"]),
        input=input_bytes,
        input_commitment=worker_module.hash_bytes("input", input_bytes),
        parent=b"p" * 32,
        prefix_digest=b"x" * 32,
        query_id="benchmark-query",
        attempt_id="benchmark-attempt",
        miner_id="benchmark-miner",
        randomness=[bytes([index + 1]) * 32 for index in range(profile["randomness_slots"])],
        certificate_profile=profile["certificate_profile"],
        kind=pb.WorkerRequest.INFERENCE,
    )


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--profile", required=True, choices=["dummy", "diffusion-small", "gemma3-1b"])
    parser.add_argument("--device", choices=["cpu", "cuda:0"], default="cpu")
    parser.add_argument("--threads", type=int, default=16)
    parser.add_argument("--requests", type=int, default=8)
    parser.add_argument("--model-ref", default=None)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.requests < 1:
        parser.error("--requests must be positive")
    if args.device == "cuda:0":
        os.environ.setdefault("CUDA_VISIBLE_DEVICES", "0")
    logical = os.cpu_count() or 1
    cpu_budget = max(1, logical // 2)
    threads = max(1, min(args.threads, cpu_budget))
    try:
        os.sched_setaffinity(0, set(range(min(cpu_budget, logical))))
    except AttributeError:
        pass
    profile_path = ROOT / "profiles" / f"{args.profile}.json"
    profile = json.loads(profile_path.read_text(encoding="utf-8"))
    before = psutil.Process().memory_info().rss
    load_started = time.perf_counter()
    worker = worker_module.ModelWorker(profile, args.device, threads, 0, args.model_ref)
    load_seconds = time.perf_counter() - load_started
    request = request_for(profile, b"Stage 2 resource benchmark input")
    worker.run(request)
    if args.device == "cuda:0":
        import torch

        torch.cuda.synchronize()
        torch.cuda.reset_peak_memory_stats()
    samples = []
    cpu_samples = []
    output_size = 0
    for index in range(args.requests):
        request.attempt_id = f"benchmark-attempt-{index}"
        started = time.perf_counter_ns()
        result = worker.run(request)
        wall_ns = time.perf_counter_ns() - started
        if not result.ok:
            raise RuntimeError(result.error)
        samples.append(wall_ns / 1_000_000)
        cpu_samples.append(result.cpu_ns / 1_000_000)
        output_size = len(result.output)
    if args.device == "cuda:0":
        import torch

        torch.cuda.synchronize()
        gpu_peak = torch.cuda.max_memory_allocated(0)
        gpu_reserved = torch.cuda.max_memory_reserved(0)
    else:
        gpu_peak = 0
        gpu_reserved = 0
    report = {
        "profile": args.profile,
        "backend": profile.get("backend", "dummy"),
        "device": args.device,
        "logical_cpus": logical,
        "cpu_budget": cpu_budget,
        "threads": threads,
        "gpu_limit": "cuda:0 only" if args.device == "cuda:0" else "none",
        "requests": args.requests,
        "model_load_seconds": load_seconds,
        "rss_before_bytes": before,
        "rss_after_bytes": psutil.Process().memory_info().rss,
        "gpu_peak_allocated_bytes": gpu_peak,
        "gpu_peak_reserved_bytes": gpu_reserved,
        "output_bytes": output_size,
        "wall_ms": {"min": min(samples), "median": statistics.median(samples), "p95": percentile(samples, 0.95), "max": max(samples)},
        "cpu_ms": {"min": min(cpu_samples), "median": statistics.median(cpu_samples), "p95": percentile(cpu_samples, 0.95), "max": max(cpu_samples)},
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
