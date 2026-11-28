#!/usr/bin/env python3
"""Run the small-diffusion EZKL circuit for PoML attempts.

The worker uses :func:`evaluate_attempt` before it computes a ticket. That
function returns the circuit's fixed-point output, so the output encrypted by
the Go miner is exactly the output later certified by EZKL. The winner path
uses :func:`prove_attempt` to create and locally check the proof. Setup files
are cached because circuit setup is much more expensive than witness creation.
"""

from __future__ import annotations

import asyncio
import hashlib
import importlib.util
import inspect
import json
import os
import struct
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable

from stage2.proof.ezkl_profile import EZKL_INPUT_SCALE, EZKL_PARAM_SCALE

ROOT = Path(__file__).resolve().parents[2]


@dataclass
class AttemptContext:
    """Prepared circuit and inputs for one model attempt."""

    profile: dict[str, Any]
    torch: Any
    ezkl: Any
    runner: Any
    graph: Any
    all_inputs: list[Any]
    model_path: Path
    settings_path: Path
    compiled_path: Path
    vk_path: Path
    srs_path: Path
    pk_path: Path


def canonical_instances(proof: dict[str, Any]) -> bytes:
    """Serialize EZKL public instances in the cross-language wire format."""

    instances = proof.get("instances")
    if not isinstance(instances, list) or not instances:
        raise ValueError("EZKL proof has no public instances")
    return json.dumps(instances, sort_keys=True, separators=(",", ":")).encode("utf-8")


def pack_model_proof(proof_bytes: bytes) -> tuple[bytes, bytes]:
    """Wrap raw EZKL JSON with the public instance bound by the certificate."""

    proof = json.loads(proof_bytes)
    instance = canonical_instances(proof)
    backend = b"ezkl"
    packed = b"PMLP" + bytes([1])
    for value in (backend, instance, proof_bytes):
        packed += struct.pack(">Q", len(value)) + value
    return packed, instance


def load_diffusion_module():
    """Load the checked-in diffusion graph without requiring package install."""

    path = ROOT / "stage2" / "proof" / "ezkl_diffusion.py"
    spec = importlib.util.spec_from_file_location("poml_ezkl_diffusion", path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load diffusion proof module from {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def call(function: Callable[..., Any], *args: Any, **kwargs: Any) -> Any:
    """Call one EZKL operation whether its binding is synchronous or async."""

    value = function(*args, **kwargs)
    if inspect.iscoroutine(value):
        value = asyncio.run(value)
    return value


def _check_inputs(randomness: list[bytes]) -> None:
    if len(randomness) != 5 or any(len(slot) != 32 for slot in randomness):
        raise ValueError("small-diffusion proof expects five 32-byte randomness slots")


def _configure_torch(torch: Any, threads: int, cpu_start: int) -> None:
    logical = os.cpu_count() or 1
    limit = max(1, logical // 2)
    threads = max(1, min(threads or limit, limit))
    try:
        available = sorted(os.sched_getaffinity(0))
        allowed = available[cpu_start : cpu_start + threads]
        if allowed:
            os.sched_setaffinity(0, allowed)
    except AttributeError:
        pass
    torch.set_num_threads(threads)
    try:
        torch.set_num_interop_threads(1)
    except RuntimeError:
        # The request worker may already have run an inference job.
        pass


def _setup_paths(cache_dir: Path) -> tuple[Path, Path, Path, Path, Path, Path]:
    return (
        cache_dir / "model.onnx",
        cache_dir / "settings.json",
        cache_dir / "model.ezkl",
        cache_dir / "vk",
        cache_dir / "srs",
        cache_dir / "pk",
    )


def _settings_match(path: Path) -> bool:
    try:
        settings = json.loads(path.read_text(encoding="utf-8"))
        run_args = settings["run_args"]
        return run_args["input_scale"] == EZKL_INPUT_SCALE and run_args["param_scale"] == EZKL_PARAM_SCALE
    except (OSError, ValueError, KeyError, TypeError):
        return False


def _setup_digest(settings_path: Path, vk_path: Path, srs_path: Path) -> str:
    digest = hashlib.sha256()
    digest.update(b"poml-stage1/v1/proof-setup/")
    for path in (settings_path, vk_path, srs_path):
        value = path.read_bytes()
        digest.update(len(value).to_bytes(8, "big"))
        digest.update(value)
    return digest.hexdigest()


def _prepare(
    profile_path: Path,
    input_bytes: bytes,
    randomness: list[bytes],
    cache_dir: Path,
    require_setup: bool,
    threads: int,
    cpu_start: int,
) -> AttemptContext:
    """Prepare the model graph and its cached EZKL circuit."""

    _check_inputs(randomness)
    diffusion = load_diffusion_module()
    profile = json.loads(profile_path.read_text(encoding="utf-8"))
    if profile.get("backend") != "diffusion-small":
        raise ValueError("EZKL attempt proving is only available for diffusion-small")
    import ezkl
    import torch

    _configure_torch(torch, threads, cpu_start)
    cache_dir.mkdir(parents=True, exist_ok=True)
    worker_module = diffusion.load_worker_module()
    runner = worker_module.TinyDenoiser(
        bytes.fromhex(profile["model_digest"]), profile["steps"], "cpu", threads, cpu_start
    )
    values = diffusion.build_inputs_for_slots(worker_module, profile, input_bytes, randomness)
    noises = values[: profile["steps"] + 1]
    biases = values[profile["steps"] + 1 :]
    graph = diffusion.make_graph(torch, runner, profile["steps"])
    all_inputs = noises + biases
    input_names = ["initial_noise"] + [f"step_noise_{index}" for index in range(profile["steps"])]
    input_names += [f"bias_{index}" for index in range(profile["steps"])]
    model_path, settings_path, compiled_path, vk_path, srs_path, pk_path = _setup_paths(cache_dir)
    if not model_path.exists():
        torch.onnx.export(
            graph,
            tuple(all_inputs),
            model_path,
            opset_version=17,
            input_names=input_names,
            output_names=["output"],
            dynamo=False,
            do_constant_folding=True,
        )
    if not settings_path.exists() or not compiled_path.exists() or not _settings_match(settings_path):
        run_args = ezkl.PyRunArgs()
        run_args.input_visibility = "private"
        run_args.output_visibility = "hashed/public"
        run_args.param_visibility = "fixed"
        run_args.input_scale = EZKL_INPUT_SCALE
        run_args.param_scale = EZKL_PARAM_SCALE
        call(ezkl.gen_settings, str(model_path), str(settings_path), py_run_args=run_args)
        call(ezkl.compile_circuit, str(model_path), str(compiled_path), str(settings_path))
    context = AttemptContext(
        profile=profile,
        torch=torch,
        ezkl=ezkl,
        runner=runner,
        graph=graph,
        all_inputs=all_inputs,
        model_path=model_path,
        settings_path=settings_path,
        compiled_path=compiled_path,
        vk_path=vk_path,
        srs_path=srs_path,
        pk_path=pk_path,
    )
    if require_setup:
        _ensure_setup(context, cache_dir)
    return context


def _ensure_setup(context: AttemptContext, cache_dir: Path) -> None:
    """Create proving and verification artifacts for the current scale."""

    marker_path = cache_dir / "setup.complete"
    marker = {"input_scale": EZKL_INPUT_SCALE, "param_scale": EZKL_PARAM_SCALE}
    ready = False
    required = (marker_path, context.settings_path, context.compiled_path, context.vk_path, context.srs_path, context.pk_path)
    if all(path.exists() for path in required):
        try:
            ready = json.loads(marker_path.read_text(encoding="utf-8")) == marker and _settings_match(context.settings_path)
        except (OSError, ValueError, TypeError):
            ready = False
    if ready:
        actual = _setup_digest(context.settings_path, context.vk_path, context.srs_path)
        if actual != context.profile.get("proof_setup_digest", "").lower():
            raise ValueError("cached EZKL artifacts do not match the profile setup digest")
        return
    settings = json.loads(context.settings_path.read_text(encoding="utf-8"))
    call(context.ezkl.gen_srs, str(context.srs_path), settings["run_args"]["logrows"])
    call(context.ezkl.setup, str(context.compiled_path), str(context.vk_path), str(context.pk_path), str(context.srs_path))
    marker_path.write_text(json.dumps(marker) + "\n", encoding="utf-8")
    actual = _setup_digest(context.settings_path, context.vk_path, context.srs_path)
    if actual != context.profile.get("proof_setup_digest", "").lower():
        raise ValueError("generated EZKL artifacts do not match the profile setup digest")


def _write_input(context: AttemptContext, output_dir: Path) -> Path:
    output_dir.mkdir(parents=True, exist_ok=True)
    input_path = output_dir / "input.json"
    input_path.write_text(
        json.dumps({"input_data": [value.detach().numpy().reshape(-1).tolist() for value in context.all_inputs]}) + "\n",
        encoding="utf-8",
    )
    return input_path


def _decode_output(witness_path: Path) -> bytes:
    """Decode the circuit's fixed-point output into canonical output bytes."""

    witness = json.loads(witness_path.read_text(encoding="utf-8"))
    outputs = witness.get("outputs")
    if not isinstance(outputs, list) or len(outputs) != 1 or not outputs[0]:
        raise ValueError("EZKL witness has no output")
    values: list[int] = []
    scale = 1 << EZKL_INPUT_SCALE
    for encoded in outputs[0]:
        value = int.from_bytes(bytes.fromhex(encoded), "little")
        if value % scale != 0 or value // scale > 255:
            raise ValueError("EZKL witness output is not a canonical byte")
        values.append(value // scale)
    return bytes(values)


def evaluate_attempt(
    profile_path: Path,
    input_bytes: bytes,
    randomness: list[bytes],
    cache_dir: Path,
    output_dir: Path,
    threads: int = 0,
    cpu_start: int = 0,
) -> tuple[bytes, dict[str, Any]]:
    """Evaluate the exact quantized circuit before ticket construction."""

    context = _prepare(
        profile_path, input_bytes, randomness, cache_dir, require_setup=False,
        threads=threads, cpu_start=cpu_start,
    )
    input_path = _write_input(context, output_dir)
    witness_path = output_dir / "witness.json"
    started = time.perf_counter()
    call(context.ezkl.gen_witness, str(input_path), str(context.compiled_path), str(witness_path))
    output = _decode_output(witness_path)
    if len(output) != int(context.profile["output_bytes"]):
        raise ValueError("EZKL witness output has the wrong byte length")
    manifest = {
        "profile_id": context.profile["id"],
        "output_bytes": len(output),
        "fixed_point_scale": EZKL_INPUT_SCALE,
        "witness_seconds": time.perf_counter() - started,
    }
    return output, manifest


def prove_attempt(
    profile_path: Path,
    input_bytes: bytes,
    randomness: list[bytes],
    cache_dir: Path,
    output_dir: Path,
    threads: int = 0,
    cpu_start: int = 0,
) -> tuple[bytes, dict[str, Any]]:
    """Create, verify and return one EZKL proof for a worker attempt."""

    context = _prepare(
        profile_path, input_bytes, randomness, cache_dir, require_setup=True,
        threads=threads, cpu_start=cpu_start,
    )
    input_path = _write_input(context, output_dir)
    proof_path = output_dir / "proof.json"
    witness_path = output_dir / "witness.json"
    started = time.perf_counter()
    call(context.ezkl.gen_witness, str(input_path), str(context.compiled_path), str(witness_path))
    circuit_output = _decode_output(witness_path)
    if len(circuit_output) != int(context.profile["output_bytes"]):
        raise ValueError("EZKL witness output has the wrong byte length")
    call(context.ezkl.prove, str(witness_path), str(context.compiled_path), str(context.pk_path), str(proof_path), str(context.srs_path))
    verified = call(context.ezkl.verify, str(proof_path), str(context.settings_path), str(context.vk_path), str(context.srs_path))
    if verified is not True:
        raise RuntimeError(f"EZKL rejected its generated attempt proof: {verified!r}")
    raw_proof = proof_path.read_bytes()
    proof_bytes, public_instance = pack_model_proof(raw_proof)
    manifest = {
        "proof_backend": "ezkl",
        "profile_id": context.profile["id"],
        "model_digest": context.profile["model_digest"],
        "proof_bytes": len(proof_bytes),
        "raw_proof_bytes": len(raw_proof),
        "public_instance_bytes": len(public_instance),
        "public_instance": public_instance.decode("utf-8"),
        "proof_verified": True,
        "output_bytes": len(circuit_output),
        "fixed_point_scale": EZKL_INPUT_SCALE,
        "proof_seconds": time.perf_counter() - started,
    }
    (output_dir / "manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return proof_bytes, manifest
