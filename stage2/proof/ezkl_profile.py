"""Consensus constants for the small-diffusion EZKL profile.

The scale is part of the circuit definition. Keeping it in one module lets
the prover, worker binding check, and peer verifier select the same encoding.
"""

EZKL_INPUT_SCALE = 13
EZKL_PARAM_SCALE = 13
