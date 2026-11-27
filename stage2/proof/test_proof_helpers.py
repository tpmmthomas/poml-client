#!/usr/bin/env python3
"""Fast tests for proof-command determinism.

The expensive proof commands are integration tests. These checks run during a
normal Stage 2 test pass and protect the byte-level inputs they consume.
"""

from __future__ import annotations

import importlib.util
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("ezkl_diffusion", ROOT / "stage2/proof/ezkl_diffusion.py")
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)
WORKER_SPEC = importlib.util.spec_from_file_location("model_worker", ROOT / "stage2/model_worker.py")
assert WORKER_SPEC and WORKER_SPEC.loader
WORKER = importlib.util.module_from_spec(WORKER_SPEC)
WORKER_SPEC.loader.exec_module(WORKER)


class ProofInputTests(unittest.TestCase):
    """Ensure proof input expansion is stable and correctly sized."""

    def test_randomness_slots_are_stable(self) -> None:
        first = MODULE.deterministic_slots(WORKER, b"vector", 5)
        second = MODULE.deterministic_slots(WORKER, b"vector", 5)
        self.assertEqual(first, second)
        self.assertEqual([len(slot) for slot in first], [32] * 5)
        self.assertNotEqual(first[0], MODULE.deterministic_slots(WORKER, b"other", 5)[0])

    def test_condition_shape_is_fixed(self) -> None:
        import torch

        condition = MODULE.condition_tensor(torch, b"vector")
        self.assertEqual(tuple(condition.shape), (1, 4))
        self.assertTrue(torch.all(condition >= -1.0))
        self.assertTrue(torch.all(condition <= 1.0))


if __name__ == "__main__":
    unittest.main()
