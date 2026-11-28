#!/usr/bin/env python3
"""Verify one EZKL model proof for a PoML peer.

The Go node sends only the raw proof and its canonical public-instance bytes.
The verifier keeps the EZKL dependency behind this process boundary. Every
peer must have the same profile, verification key, settings, and SRS cache.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import sys
import tempfile
from pathlib import Path
from typing import Any

# The Go node invokes this file by absolute path. Add the repository root so
# the checked-in profile constants are importable without package installation.
REPOSITORY_ROOT = Path(__file__).resolve().parents[2]
if str(REPOSITORY_ROOT) not in sys.path:
    sys.path.insert(0, str(REPOSITORY_ROOT))

from stage2.proof.ezkl_profile import EZKL_INPUT_SCALE, EZKL_PARAM_SCALE


def canonical_instances(proof: dict[str, Any]) -> bytes:
    instances = proof.get("instances")
    if not isinstance(instances, list) or not instances:
        raise ValueError("EZKL proof has no public instances")
    return json.dumps(instances, sort_keys=True, separators=(",", ":")).encode("utf-8")


def tagged_hash(domain: str, *parts: bytes) -> bytes:
    digest = hashlib.sha256()
    digest.update(b"poml-stage1/v1/" + domain.encode("utf-8") + b"/")
    for part in parts:
        digest.update(len(part).to_bytes(8, "big"))
        digest.update(part)
    return digest.digest()


def verify(request: dict[str, Any], profiles_dir: Path, cache_dir: Path) -> None:
    profile_id = request.get("model_profile_id", "")
    proof_profile = request.get("proof_profile", "")
    profile_path = profiles_dir / f"{profile_id}.json"
    if not profile_path.is_file():
        raise ValueError(f"model profile is unavailable: {profile_path}")
    profile = json.loads(profile_path.read_text(encoding="utf-8"))
    if profile.get("certificate_profile") != proof_profile:
        raise ValueError("proof profile does not match the model profile")
    if profile.get("backend") != "diffusion-small" or proof_profile != "zk-ezkl-diffusion-v1":
        raise ValueError("the configured verifier only supports zk-ezkl-diffusion-v1")
    expected_setup = profile.get("proof_setup_digest", "")
    if not expected_setup or request.get("setup_digest", "").lower() != expected_setup.lower():
        raise ValueError("setup digest is missing")

    settings = cache_dir / "settings.json"
    verification_key = cache_dir / "vk"
    srs = cache_dir / "srs"
    for artifact in (settings, verification_key, srs):
        if not artifact.is_file():
            raise ValueError(f"EZKL verification artifact is missing: {artifact}")
    settings_data = json.loads(settings.read_text(encoding="utf-8"))
    run_args = settings_data.get("run_args", {})
    if run_args.get("input_scale") != EZKL_INPUT_SCALE or run_args.get("param_scale") != EZKL_PARAM_SCALE:
        raise ValueError("EZKL setup uses an unexpected fixed-point scale")
    actual_setup = tagged_hash("proof-setup", settings.read_bytes(), verification_key.read_bytes(), srs.read_bytes()).hex()
    if actual_setup != expected_setup.lower():
        raise ValueError("configured EZKL artifacts do not match the pinned setup digest")

    try:
        proof_bytes = base64.b64decode(request["proof_base64"], validate=True)
        expected_instance = base64.b64decode(request["public_instance_base64"], validate=True)
    except (KeyError, ValueError) as error:
        raise ValueError(f"invalid proof encoding: {error}") from error
    with tempfile.TemporaryDirectory(prefix="poml-verify-") as temporary:
        proof_path = Path(temporary) / "proof.json"
        proof_path.write_bytes(proof_bytes)
        try:
            proof = json.loads(proof_bytes)
        except json.JSONDecodeError as error:
            raise ValueError(f"EZKL proof is not JSON: {error}") from error
        if canonical_instances(proof) != expected_instance:
            raise ValueError("proof public instances do not match the bound instance")
        import ezkl

        if ezkl.verify(str(proof_path), str(settings), str(verification_key), str(srs)) is not True:
            raise ValueError("EZKL rejected the model proof")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profiles-dir", type=Path, required=True)
    parser.add_argument("--cache-dir", type=Path, required=True)
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    try:
        request = json.load(sys.stdin)
        verify(request, args.profiles_dir, args.cache_dir)
    except Exception as error:
        print(json.dumps({"ok": False, "error": str(error)}), file=sys.stderr)
        raise SystemExit(1) from error
    print(json.dumps({"ok": True}, separators=(",", ":")))


if __name__ == "__main__":
    main()
