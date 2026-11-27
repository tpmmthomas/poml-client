#!/usr/bin/env python3
"""Generate and verify an EZKL proof for the small PoML diffusion profile.

This command is deliberately separate from ``model_worker.py``.  A worker
must answer many inference requests, while circuit setup and proving are
expensive jobs that belong in a certification queue.  The command uses the
same TinyDenoiser weights, condition construction, randomness expansion and
four-step scheduler as the worker, then proves the exported fixed-shape graph
with EZKL.

The graph takes the four conditioning-and-timestep bias maps as private
inputs.  EZKL 23.0.5 cannot keep the bias scale aligned when it computes the
linear conditioning layers inside a convolutional graph.  The manifest calls
this out as ``conditioning_scope=precomputed_bias``.  The proof therefore
certifies the denoising core and its scheduler, while a future profile must
add a separately proved conditioning relation before it can be used as the
complete PoML winner proof.

EZKL exposes the model output as a public instance in this profile.  That is
useful for validating the proving pipeline, but it is not the final hidden
output-commitment relation from the paper.  The script never changes the Go
node's temporary random-certificate policy.
"""

from __future__ import annotations

import argparse
import asyncio
import hashlib
import importlib.util
import inspect
import json
import os
import time
from pathlib import Path
from typing import Any, Callable


ROOT = Path(__file__).resolve().parents[2]
DEFAULT_PROFILE = ROOT / "profiles" / "diffusion-small.json"


def load_worker_module():
    """Load the checked-in worker without requiring package installation."""

    path = ROOT / "stage2" / "model_worker.py"
    spec = importlib.util.spec_from_file_location("poml_stage2_model_worker", path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load worker module from {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def call(name: str, function: Callable[..., Any], *args: Any, **kwargs: Any) -> tuple[Any, float]:
    """Call one EZKL operation and return its result and wall time in seconds."""

    started = time.perf_counter()
    value = function(*args, **kwargs)
    if inspect.iscoroutine(value):
        value = asyncio.run(value)
    return value, time.perf_counter() - started


def condition_tensor(torch: Any, input_bytes: bytes) -> Any:
    """Reproduce TinyDenoiser's four-element conditioning vector exactly."""

    condition_bytes = hashlib.sha256(b"poml-diffusion-condition/" + input_bytes).digest()[:16]
    values = [
        (int.from_bytes(condition_bytes[index : index + 4], "big") / 2**31) - 1
        for index in range(0, 16, 4)
    ]
    return torch.tensor(values, dtype=torch.float32)[None, :]


def deterministic_slots(worker: Any, input_bytes: bytes, count: int) -> list[bytes]:
    """Create a reproducible proof vector using the worker's tagged hash."""

    return [worker.hash_bytes("proof-randomness", input_bytes, index.to_bytes(4, "big")) for index in range(count)]


def build_inputs(worker: Any, profile: dict[str, Any], input_bytes: bytes) -> tuple[list[Any], bytes]:
    """Build the exact initial noise, step noises and bias maps for one attempt."""

    import torch

    model = worker.TinyDenoiser(
        bytes.fromhex(profile["model_digest"]), profile["steps"], "cpu", 16, 0
    )
    slots = deterministic_slots(worker, input_bytes, profile["randomness_slots"])
    noises = [model._noise(slot) for slot in slots]
    condition = condition_tensor(torch, input_bytes)
    biases: list[Any] = []
    for step in range(profile["steps"]):
        timestep = torch.tensor(
            [[step / max(1, profile["steps"] - 1)] * 4], dtype=torch.float32
        )
        bias = model.network.condition(condition) + model.network.timestep(timestep)
        biases.append(bias[:, :, None, None].expand(1, 8, 8, 8).contiguous())
    return noises + biases, slots[0]


def make_graph(torch: Any, runner: Any, steps: int) -> Any:
    """Create an ONNX-exportable module for the exact four-step core."""

    from torch import nn

    class ProofGraph(nn.Module):
        def __init__(self) -> None:
            super().__init__()
            self.conv1 = runner.network.conv1
            self.conv2 = runner.network.conv2
            self.conv3 = runner.network.conv3

        def denoise(self, x: Any, bias: Any) -> Any:
            x = torch.tanh(self.conv1(x) + bias)
            x = torch.tanh(self.conv2(x))
            return self.conv3(x)

        def forward(self, x: Any, *values: Any) -> Any:
            noises = values[:steps]
            biases = values[steps:]
            for step, (noise, bias) in enumerate(zip(noises, biases)):
                predicted = self.denoise(x, bias)
                x = 0.82 * x - 0.18 * predicted + (0.12 / (step + 1)) * noise
            return torch.clamp(torch.round((torch.tanh(x) + 1.0) * 127.5), 0, 255)

    return ProofGraph().eval()


def write_json(path: Path, value: Any) -> None:
    """Write readable, stable JSON for a proof run."""

    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def artifact_sizes(directory: Path) -> dict[str, int]:
    """Return sizes for files left in the proof directory."""

    return {
        path.name: path.stat().st_size
        for path in sorted(directory.iterdir())
        if path.is_file() and path.name != "manifest.json"
    }


def verify_tampering(ezkl: Any, directory: Path) -> bool:
    """Mutate one public instance and require EZKL verification to reject it."""

    original = directory / "proof.json"
    mutated = directory / "proof-mutated.json"
    proof = json.loads(original.read_text(encoding="utf-8"))
    instances = proof.get("instances")
    if not instances or not instances[0]:
        raise RuntimeError("EZKL proof has no public instance to mutate")
    first = instances[0][0]
    instances[0][0] = ("1" if not first.startswith("1") else "2") + first[1:]
    write_json(mutated, proof)
    try:
        result = ezkl.verify(
            str(mutated), str(directory / "settings.json"), str(directory / "vk"), str(directory / "srs")
        )
        rejected = result is False
    except Exception:
        rejected = True
    mutated.unlink(missing_ok=True)
    return rejected


def run(args: argparse.Namespace) -> dict[str, Any]:
    """Run export, setup, proving, verification and tamper testing."""

    os.environ.setdefault("OMP_NUM_THREADS", "16")
    os.environ.setdefault("MKL_NUM_THREADS", "16")
    os.environ.setdefault("OPENBLAS_NUM_THREADS", "16")
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=True)
    profile = json.loads(args.profile.read_text(encoding="utf-8"))
    if profile.get("id") != "diffusion-small":
        raise ValueError("this proof command is pinned to profiles/diffusion-small.json")

    worker = load_worker_module()
    import torch

    torch.set_num_threads(16)
    torch.set_num_interop_threads(1)
    runner = worker.TinyDenoiser(bytes.fromhex(profile["model_digest"]), profile["steps"], "cpu", 16, 0)
    input_bytes = args.input.encode("utf-8")
    values, _ = build_inputs(worker, profile, input_bytes)
    noises = values[: profile["steps"]]
    biases = values[profile["steps"] :]
    graph = make_graph(torch, runner, profile["steps"])
    all_inputs = noises + biases
    input_names = ["initial_noise"] + [f"step_noise_{i}" for i in range(profile["steps"])]
    input_names += [f"bias_{i}" for i in range(profile["steps"])]
    onnx_path = output / "model.onnx"
    started = time.perf_counter()
    torch.onnx.export(
        graph,
        tuple(all_inputs),
        onnx_path,
        opset_version=17,
        input_names=input_names,
        output_names=["output"],
        dynamo=False,
        do_constant_folding=True,
    )
    export_seconds = time.perf_counter() - started
    input_json = output / "input.json"
    write_json(
        input_json,
        {"input_data": [value.detach().numpy().reshape(-1).tolist() for value in all_inputs]},
    )

    with torch.inference_mode():
        expected = graph(*all_inputs).to(torch.uint8).numpy().tobytes()
    reference, _, _ = runner.infer(input_bytes, deterministic_slots(worker, input_bytes, profile["randomness_slots"]))
    if expected != reference:
        raise RuntimeError("exported graph does not match the Stage 2 TinyDenoiser output")

    import ezkl

    run_args = ezkl.PyRunArgs()
    run_args.input_visibility = "private"
    run_args.output_visibility = "public"
    run_args.param_visibility = "fixed"
    run_args.input_scale = 13
    run_args.param_scale = 13

    timings: dict[str, float] = {"export": export_seconds}
    _, timings["settings"] = call("settings", ezkl.gen_settings, str(onnx_path), str(output / "settings.json"), py_run_args=run_args)
    settings = json.loads((output / "settings.json").read_text(encoding="utf-8"))
    logrows = settings["run_args"]["logrows"]
    _, timings["compile"] = call(
        "compile", ezkl.compile_circuit, str(onnx_path), str(output / "model.ezkl"), str(output / "settings.json")
    )
    _, timings["witness"] = call(
        "witness", ezkl.gen_witness, str(input_json), str(output / "model.ezkl"), str(output / "witness.json")
    )
    _, timings["srs"] = call("srs", ezkl.gen_srs, str(output / "srs"), logrows)
    _, timings["setup"] = call(
        "setup", ezkl.setup, str(output / "model.ezkl"), str(output / "vk"), str(output / "pk"), str(output / "srs")
    )
    _, timings["prove"] = call(
        "prove", ezkl.prove, str(output / "witness.json"), str(output / "model.ezkl"), str(output / "pk"), str(output / "proof.json"), str(output / "srs")
    )
    verified, timings["verify"] = call(
        "verify", ezkl.verify, str(output / "proof.json"), str(output / "settings.json"), str(output / "vk"), str(output / "srs")
    )
    if verified is not True:
        raise RuntimeError(f"EZKL returned {verified!r} for the generated proof")
    tamper_rejected = verify_tampering(ezkl, output)
    if not tamper_rejected:
        raise RuntimeError("mutated public instance was accepted by EZKL")

    if not args.keep_prover_key:
        (output / "pk").unlink(missing_ok=True)
    if not args.keep_witness:
        (output / "witness.json").unlink(missing_ok=True)

    manifest = {
        "proof_backend": "ezkl",
        "ezkl_version": getattr(ezkl, "__version__", "unknown"),
        "profile_id": profile["id"],
        "model_digest": profile["model_digest"],
        "input": args.input,
        "steps": profile["steps"],
        "conditioning_scope": "precomputed_bias",
        "output_visibility": "public",
        "relation_scope": "four-step TinyDenoiser denoising core and scheduler",
        "poML_complete_relation": False,
        "proof_verified": True,
        "mutated_public_instance_rejected": tamper_rejected,
        "quantization": {"input_scale": 13, "param_scale": 13, "logrows": logrows},
        "timings_seconds": timings,
        "artifact_bytes": artifact_sizes(output),
        "prover_key_retained": args.keep_prover_key,
        "witness_retained": args.keep_witness,
    }
    write_json(output / "manifest.json", manifest)
    return manifest


def parse_args() -> argparse.Namespace:
    """Parse command-line options."""

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", type=Path, default=DEFAULT_PROFILE)
    parser.add_argument("--input", default="a proof-vector diffusion request")
    parser.add_argument("--output-dir", type=Path, default=Path("proof-runs/diffusion-small"))
    parser.add_argument("--keep-prover-key", action="store_true")
    parser.add_argument("--keep-witness", action="store_true")
    return parser.parse_args()


if __name__ == "__main__":
    result = run(parse_args())
    print(json.dumps(result, indent=2, sort_keys=True))
