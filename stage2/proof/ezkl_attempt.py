#!/usr/bin/env python3
"""Prove one small-diffusion attempt for the PoML worker.

The setup artifacts are cached because EZKL setup is much more expensive than
an individual witness and proof. The function returns the proof JSON bytes and
does not expose the model output as a public instance. The Go node still needs
an in-process verifier for this profile before it is suitable for adversarial
consensus. Until then the proof is carried in the versioned bundle and checked
by the local worker.
"""

from __future__ import annotations

import asyncio
import importlib.util
import inspect
import json
import time
from pathlib import Path
from typing import Any, Callable


ROOT = Path(__file__).resolve().parents[2]


def load_diffusion_module():
    path = ROOT / "stage2" / "proof" / "ezkl_diffusion.py"
    spec = importlib.util.spec_from_file_location("poml_ezkl_diffusion", path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load diffusion proof module from {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def call(function: Callable[..., Any], *args: Any, **kwargs: Any) -> Any:
    value = function(*args, **kwargs)
    if inspect.iscoroutine(value):
        value = asyncio.run(value)
    return value


def prove_attempt(
    profile_path: Path,
    input_bytes: bytes,
    randomness: list[bytes],
    cache_dir: Path,
    output_dir: Path,
) -> tuple[bytes, dict[str, Any]]:
    """Create, verify and return one EZKL proof for a worker attempt."""

    if len(randomness) != 5 or any(len(slot) != 32 for slot in randomness):
        raise ValueError("small-diffusion proof expects five 32-byte randomness slots")
    diffusion = load_diffusion_module()
    profile = json.loads(profile_path.read_text(encoding="utf-8"))
    if profile.get("backend") != "diffusion-small":
        raise ValueError("EZKL attempt proving is only available for diffusion-small")
    import torch
    import ezkl

    torch.set_num_threads(16)
    try:
        torch.set_num_interop_threads(1)
    except RuntimeError:
        # The request worker may already have run an inference job.
        pass
    output_dir.mkdir(parents=True, exist_ok=True)
    cache_dir.mkdir(parents=True, exist_ok=True)
    runner = diffusion.load_worker_module().TinyDenoiser(
        bytes.fromhex(profile["model_digest"]), profile["steps"], "cpu", 16, 0
    )
    values = diffusion.build_inputs_for_slots(
        diffusion.load_worker_module(), profile, input_bytes, randomness
    )
    noises = values[: profile["steps"] + 1]
    biases = values[profile["steps"] + 1 :]
    graph = diffusion.make_graph(torch, runner, profile["steps"])
    all_inputs = noises + biases
    input_names = ["initial_noise"] + [f"step_noise_{index}" for index in range(profile["steps"])]
    input_names += [f"bias_{index}" for index in range(profile["steps"])]
    model_path = cache_dir / "model.onnx"
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

    settings_path = cache_dir / "settings.json"
    compiled_path = cache_dir / "model.ezkl"
    vk_path = cache_dir / "vk"
    srs_path = cache_dir / "srs"
    pk_path = cache_dir / "pk"
    setup_marker = cache_dir / "setup.complete"
    if not setup_marker.exists():
        run_args = ezkl.PyRunArgs()
        run_args.input_visibility = "private"
        run_args.output_visibility = "hashed/public"
        run_args.param_visibility = "fixed"
        run_args.input_scale = 13
        run_args.param_scale = 13
        call(ezkl.gen_settings, str(model_path), str(settings_path), py_run_args=run_args)
        settings = json.loads(settings_path.read_text(encoding="utf-8"))
        call(ezkl.compile_circuit, str(model_path), str(compiled_path), str(settings_path))
        call(ezkl.gen_srs, str(srs_path), settings["run_args"]["logrows"])
        call(ezkl.setup, str(compiled_path), str(vk_path), str(pk_path), str(srs_path))
        setup_marker.write_text("ok\n", encoding="utf-8")

    input_path = output_dir / "input.json"
    proof_path = output_dir / "proof.json"
    witness_path = output_dir / "witness.json"
    input_path.write_text(
        json.dumps({"input_data": [value.detach().numpy().reshape(-1).tolist() for value in all_inputs]}) + "\n",
        encoding="utf-8",
    )
    with torch.inference_mode():
        expected = graph(*all_inputs).to(torch.uint8).numpy().tobytes()
    started = time.perf_counter()
    call(ezkl.gen_witness, str(input_path), str(compiled_path), str(witness_path))
    call(ezkl.prove, str(witness_path), str(compiled_path), str(pk_path), str(proof_path), str(srs_path))
    verified = call(ezkl.verify, str(proof_path), str(settings_path), str(vk_path), str(srs_path))
    if verified is not True:
        raise RuntimeError(f"EZKL rejected its generated attempt proof: {verified!r}")
    proof_bytes = proof_path.read_bytes()
    manifest = {
        "proof_backend": "ezkl",
        "profile_id": profile["id"],
        "model_digest": profile["model_digest"],
        "proof_bytes": len(proof_bytes),
        "proof_verified": True,
        "output_bytes": len(expected),
        "proof_seconds": time.perf_counter() - started,
    }
    (output_dir / "manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return proof_bytes, manifest
