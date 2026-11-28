# Stage 2 proof status

This is the short answer to the proof questions.

## What the Go node verifies now

For `zk-ezkl-diffusion-v1`, the Go node accepts a block only after these
checks succeed.

1. The block and winner context are valid.
2. The auxiliary certificate is an Ed25519 signature over the winner statement
   and the EZKL public-instance bytes.
3. The Python verifier checks the raw EZKL proof with the configured settings,
   verification key and SRS.
4. The proof's public instance is byte-for-byte the instance signed by the
   auxiliary certificate.
5. The setup files have the digest pinned in the consensus profile.

The Python process is started by the Go verifier through `ProcessVerifier`. A
missing interpreter, missing setup file, timeout, malformed response or proof
failure rejects the block.

## What was wrong before

The old path checked that a wrapper contained proof-looking bytes. It did not
run EZKL on those bytes. It also let normal full-precision model output differ
from the fixed-point output used by the EZKL circuit. That meant the ticket,
ciphertext and output commitment could describe bytes different from the
proved result.

The current worker evaluates the EZKL fixed-point witness before it computes a
ticket. The same canonical bytes are encrypted, committed and supplied to the
winner prover. The generated proof is packed in `PMLP`, and its public instance
is signed in the auxiliary certificate. A tampered proof or instance is
rejected by the local block validator and by peers.

## How the two proof layers fit together

The model layer is the EZKL proof of the four-step small diffusion denoising
graph and scheduler. The auxiliary layer checks the PoML transcript. It
verifies the inference VRF, encryption VRF, randomness expansion, query
opening, output commitment and deterministic HPKE ciphertext.

They meet at one value, the EZKL public instance for the canonical model
output. The auxiliary certificate signs that instance together with the block
statement. This is a real, independently checkable composed proof path.

## What is still different from the final paper relation

The two checks are not yet one zero-knowledge relation. EZKL does not currently
prove the SHA-256 output commitment, ECVRF verification, randomness expansion
or HPKE arithmetic inside its circuit. The auxiliary check is an Ed25519
certificate over a host-side witness. It therefore relies on the configured
certificate key and is not the trustless ZK auxiliary proof described by the
final paper plan.

The current diffusion circuit also receives the conditioning and timestep bias
maps as private precomputed inputs. It does not yet prove their derivation from
the query inside the circuit. The Gemma DeepProve proof remains a standalone
experiment and is not yet a node-integrated `zk-` profile. MobileDiffusion is
still blocked by the current EZKL scale limitation.

The sound way to reach the final relation is to keep this public statement and
replace the certificate with an outer proof that verifies the model proof and
the auxiliary VRF, commitment and HPKE relations, or to build one circuit that
contains all of them. The current interfaces and public-instance binding are
the composition boundary for that later replacement.

## Local run

Prepare a cache containing the exact `settings.json`, `vk`, `srs`, `pk`,
`model.ezkl` and `model.onnx` files whose digest appears in
`profiles/diffusion-small-ezkl.json`. Then run:

```bash
POML_EZKL_CACHE=/path/to/prepared-cache \
  ./scripts/run_devnet.sh run/diffusion-ezkl
```

The launcher links every node to that cache. The profile digest prevents a
peer from silently using a different circuit setup.
