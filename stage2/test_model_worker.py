#!/usr/bin/env python3
"""Small, dependency-light self-checks for the Stage 2 model worker."""

from __future__ import annotations

import importlib.util
import json
import os
import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MODULE_SPEC = importlib.util.spec_from_file_location("poml_model_worker", ROOT / "stage2" / "model_worker.py")
assert MODULE_SPEC and MODULE_SPEC.loader
worker_module = importlib.util.module_from_spec(MODULE_SPEC)
MODULE_SPEC.loader.exec_module(worker_module)


def request_for(profile: dict, worker, backend_input: bytes):
    from api.proto import poml_pb2 as pb

    return pb.WorkerRequest(
        version=1,
        profile_id=profile["id"],
        profile_digest=worker_module.profile_digest(profile),
        model_digest=bytes.fromhex(profile["model_digest"]),
        input=backend_input,
        input_commitment=worker_module.hash_bytes("input", backend_input),
        parent=b"p" * 32,
        prefix_digest=b"x" * 32,
        query_id="test-query",
        attempt_id="test-attempt",
        miner_id="test-miner",
        randomness=[bytes([index + 1]) * 32 for index in range(profile["randomness_slots"])],
        certificate_profile=profile["certificate_profile"],
        kind=pb.WorkerRequest.INFERENCE,
    )


class WorkerTests(unittest.TestCase):
    def load(self, profile_id: str):
        with (ROOT / "profiles" / f"{profile_id}.json").open(encoding="utf-8") as handle:
            return json.load(handle)

    def test_dummy_is_repeatable_and_proof_is_random(self):
        profile = self.load("dummy")
        worker = worker_module.ModelWorker(profile, "cpu", 1, 0, None)
        first = worker.run(request_for(profile, worker, b"dummy input"))
        second = worker.run(request_for(profile, worker, b"dummy input"))
        self.assertTrue(first.ok, first.error)
        self.assertEqual(first.output, second.output)
        from api.proto import poml_pb2 as pb

        proof_request = pb.WorkerRequest(
            version=1,
            profile_id=profile["id"],
            profile_digest=worker_module.profile_digest(profile),
            certificate_profile=profile["certificate_profile"],
            kind=pb.WorkerRequest.CERTIFICATE,
            statement=b"statement",
            witness=pb.Witness(output=first.output),
        )
        proof_a = worker.run(proof_request)
        proof_b = worker.run(proof_request)
        self.assertTrue(proof_a.ok, proof_a.error)
        self.assertEqual(len(proof_a.certificate), 64)
        self.assertNotEqual(proof_a.certificate, proof_b.certificate)

    def test_profile_digest_vectors(self):
        expected = {
            "dummy": "0ea8e1766be2f45129de53e279c3351b862f3208ce1d4260e74cc1af06fd30fd",
            "diffusion-small": "6f49a544845bfd02ed0696c2db4ca27ecff220a8847d68ea7e7a490d98fccc83",
            "diffusion-small-ezkl": "c60c1ee21215a473a07cbbcd7dac35ff3482beab287e4dc7da311246670d4cd8",
            "gemma3-1b": "bad1f00d581829f27239e2a8f923a1f31cf4a734bb5eed817507ede94fcccc3b",
        }
        for profile_id, digest in expected.items():
            self.assertEqual(worker_module.profile_digest(self.load(profile_id)).hex(), digest)

    def test_diffusion_is_repeatable(self):
        profile = self.load("diffusion-small")
        worker = worker_module.ModelWorker(profile, "cpu", 4, 0, None)
        first = worker.run(request_for(profile, worker, b"diffusion input"))
        second = worker.run(request_for(profile, worker, b"diffusion input"))
        self.assertTrue(first.ok, first.error)
        self.assertEqual(first.output, second.output)
        self.assertEqual(list(first.output_shape), [1, 8, 8])
        self.assertEqual(len(first.output), 64)

    @unittest.skipUnless(os.environ.get("POML_GEMMA_MODEL"), "set POML_GEMMA_MODEL to run the Gemma checkpoint test")
    def test_gemma_checkpoint(self):
        profile = self.load("gemma3-1b")
        worker = worker_module.ModelWorker(profile, "cpu", 16, 0, os.environ["POML_GEMMA_MODEL"])
        result = worker.run(request_for(profile, worker, b"Explain PoML in one sentence."))
        self.assertTrue(result.ok, result.error)
        self.assertGreater(len(result.output), 0)
        self.assertEqual(len(result.output) % 4, 0)
        self.assertEqual(list(result.output_shape), [len(result.output)])


if __name__ == "__main__":
    unittest.main()
