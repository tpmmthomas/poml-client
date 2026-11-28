# Proof envelope

This package owns the canonical `PMLB` proof bundle. It records a profile ID,
setup digest, statement digest, model-proof bytes and binding-proof bytes. The
envelope is length-delimited and bounded to keep malformed peer messages from
allocating unbounded memory.

EZKL model bytes use a nested `PMLP` envelope. It records the backend name,
the canonical public-instance JSON and the raw backend proof. The auxiliary
certificate signs the PoML statement together with this public instance. This
shared value is the composition point between the model proof and the VRF,
commitment and HPKE proof layer.

`aux-ed25519-v1` uses an Ed25519 binding proof after the Go auxiliary witness
checker validates the inference VRF, encryption VRF, output commitment and
deterministic HPKE ciphertext. `zk-ezkl-diffusion-v1` places an EZKL proof in
the model-proof field. `ProcessVerifier` invokes the configured Python EZKL
verifier for every peer. The verifier requires the exact profile and setup
cache, then checks both the proof and its bound public instance.
