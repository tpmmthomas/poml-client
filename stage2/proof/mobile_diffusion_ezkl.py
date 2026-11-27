#!/usr/bin/env python3
"""Export and probe the real MobileDiffusion denoiser with EZKL.

The command loads the pinned MobileDiffusionTiny UNet, runs one fixed latent
denoising call and exports that exact 8x8 graph. It then asks the installed
EZKL version to compile the graph. The current EZKL release rejects the graph
while assigning fixed-point scales to convolution biases, so the normal result
is a manifest with ``proof_status=blocked`` and the precise compiler error.

This is intentionally a probe rather than a fake certificate. A successful
ONNX export only says that PyTorch can describe the model. It does not prove a
MobileDiffusion image. The complete paper profile still needs the text encoder,
initial noise, scheduler, UNet, VAE decoder and output commitment in one
versioned relation.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import time
from pathlib import Path
from typing import Any


def sha256_file(path: Path) -> str:
    """Hash one checkpoint file in bounded memory."""

    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def model_manifest(model_dir: Path) -> dict[str, str]:
    """Return hashes for the model files that affect this probe."""

    names = [
        "unet/config.json",
        "unet/diffusion_pytorch_model.safetensors",
        "text_encoder/config.json",
        "text_encoder/model.safetensors",
        "vae/config.json",
        "vae/diffusion_pytorch_model.safetensors",
        "scheduler/scheduler_config.json",
        "tokenizer/tokenizer_config.json",
        "tokenizer/vocab.json",
        "tokenizer/merges.txt",
    ]
    return {
        name: sha256_file(model_dir / name)
        for name in names
        if (model_dir / name).is_file()
    }


def write_json(path: Path, value: Any) -> None:
    """Write stable human-readable JSON."""

    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def run(args: argparse.Namespace) -> dict[str, Any]:
    """Run inference, export the denoiser and record EZKL compatibility."""

    os.environ.setdefault("OMP_NUM_THREADS", "16")
    os.environ.setdefault("MKL_NUM_THREADS", "16")
    os.environ.setdefault("OPENBLAS_NUM_THREADS", "16")
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=True)

    import torch
    from diffusers import UNet2DConditionModel

    torch.set_num_threads(16)
    torch.set_num_interop_threads(1)
    model_dir = args.model_dir.resolve() if args.model_dir.exists() else args.model_dir
    started = time.perf_counter()
    unet = UNet2DConditionModel.from_pretrained(
        str(model_dir), subfolder="unet", local_files_only=args.local_only
    ).eval()
    load_seconds = time.perf_counter() - started
    torch.manual_seed(args.seed)
    sample = torch.randn(1, 4, 8, 8) * 0.1
    timestep = torch.tensor([999.0])
    hidden = torch.randn(1, 77, 512) * 0.05
    with torch.inference_mode():
        started = time.perf_counter()
        output_tensor = unet(sample, timestep, hidden).sample
        inference_seconds = time.perf_counter() - started

    onnx_path = output / "mobile-unet.onnx"
    started = time.perf_counter()
    torch.onnx.export(
        unet,
        (sample, timestep, hidden),
        onnx_path,
        opset_version=17,
        input_names=["sample", "timestep", "encoder_hidden_states"],
        output_names=["noise_pred"],
        dynamo=False,
        do_constant_folding=True,
    )
    export_seconds = time.perf_counter() - started
    write_json(
        output / "input.json",
        {
            "input_data": [
                sample.numpy().reshape(-1).tolist(),
                timestep.numpy().reshape(-1).tolist(),
                hidden.numpy().reshape(-1).tolist(),
            ]
        },
    )

    proof_status = "blocked"
    compiler_error = ""
    try:
        import ezkl

        run_args = ezkl.PyRunArgs()
        run_args.input_visibility = "private"
        run_args.output_visibility = "public"
        run_args.param_visibility = "fixed"
        run_args.input_scale = 13
        run_args.param_scale = 13
        ezkl.gen_settings(str(onnx_path), str(output / "settings.json"), py_run_args=run_args)
        proof_status = "settings-generated"
    except BaseException as error:
        compiler_error = f"{type(error).__name__}: {error}"

    manifest = {
        "backend": "ezkl",
        "model_reference": str(args.model_dir),
        "model_files": model_manifest(args.model_dir) if args.model_dir.is_dir() else {},
        "latent_shape": [1, 4, 8, 8],
        "text_condition_shape": [1, 77, 512],
        "timestep": 999,
        "seed": args.seed,
        "load_seconds": load_seconds,
        "inference_seconds": inference_seconds,
        "onnx_export_seconds": export_seconds,
        "onnx_bytes": onnx_path.stat().st_size,
        "proof_status": proof_status,
        "compiler_error": compiler_error,
        "proof_verified": False,
        "complete_pipeline_proved": False,
        "components_attempted": ["UNet denoiser"],
        "components_pending": ["CLIP text encoder", "scheduler", "VAE decoder", "output commitment", "VRF/HPKE binding"],
        "resource_policy": {"cpu_threads": 16, "gpu": "none"},
    }
    write_json(output / "manifest.json", manifest)
    return manifest


def parse_args() -> argparse.Namespace:
    """Parse command-line options."""

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--model-dir",
        default=os.environ.get("POML_MOBILE_DIFFUSION_DIR"),
        help="local MobileDiffusionTiny directory, or a Hugging Face model ID",
    )
    parser.add_argument("--output-dir", type=Path, default=Path("proof-runs/mobile-diffusion"))
    parser.add_argument("--seed", type=int, default=17)
    parser.add_argument("--local-only", action="store_true")
    args = parser.parse_args()
    if not args.model_dir:
        parser.error("--model-dir or POML_MOBILE_DIFFUSION_DIR is required")
    args.model_dir = Path(args.model_dir)
    return args


if __name__ == "__main__":
    print(json.dumps(run(parse_args()), indent=2, sort_keys=True))
