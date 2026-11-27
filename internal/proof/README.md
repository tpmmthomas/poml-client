# Proof envelope

This package owns the canonical `PMLB` proof bundle. It records a profile ID,
setup digest, statement digest, model-proof bytes and binding-proof bytes. The
envelope is length-delimited and bounded to keep malformed peer messages from
allocating unbounded memory.

`aux-ed25519-v1` uses an Ed25519 binding proof after the Go auxiliary witness
checker validates the inference VRF, encryption VRF, output commitment and
deterministic HPKE ciphertext. `zk-ezkl-diffusion-v1` places an EZKL proof in
the model-proof field. A native peer-side verifier for that model proof is
still required before treating it as a fully trustless consensus profile.
