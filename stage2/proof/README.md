# Real model-proof experiments

This directory contains opt-in proving commands. The normal node uses the
Stage 1 fixture or auxiliary profile when selected. The
`diffusion-small-ezkl` profile evaluates its compiled circuit for each attempt
and creates a full proof only for a winning attempt.

## What is real today

`ezkl_diffusion.py` exports the exact four-step `diffusion-small` denoising
core used by `stage2/model_worker.py`, generates an EZKL proving key, creates a
Halo2 proof, verifies it, and checks that changing the public output commitment
makes verification fail. The output is a real model proof for the fixed-point
graph. The output pixels stay private in the proof. EZKL exposes only a
Poseidon hash of the quantized output as a public instance.
Its manifest says `conditioning_scope=precomputed_bias` because EZKL 23.0.5
cannot keep the convolution bias scales aligned when the conditioning linear
layers are placed inside this graph. The graph therefore proves the denoising
core and scheduler, not the derivation of each bias map from the query.

`ezkl_attempt.py` uses the same graph with the actual PoML randomness slots and
returns a `PMLP` envelope containing the raw proof and canonical public
instance. The worker evaluates the circuit witness before the ticket is
computed. That is important because EZKL fixed-point arithmetic can differ by
one byte from an ordinary full-precision PyTorch evaluation. Setup artifacts
are cached because setup is much slower than an individual witness or proof.

The PoML worker now binds this public instance into the auxiliary certificate.
The configured peer verifier runs EZKL independently and checks that the proof
contains the same instance. This composes the model proof with the protocol's
tagged SHA-256 output commitment, deterministic HPKE ciphertext and VRF
transcript. The composition is independently checkable, while the current
EZKL circuit still proves the denoising core rather than implementing the
SHA-256, ECVRF and HPKE arithmetic inside one circuit.

`run_deepprove_gemma.sh` builds the pinned DeepProve Gemma backend, proves and
verifies a bounded Gemma 3 inference, and exports the proof bytes. DeepProve's
model loader supports Gemma 3 SafeTensors and proves the transformer through
the next-token argmax. Its upstream benchmark verifies the proof before the
command exits. The script adds only a local `--proof-out` option to the pinned
checkout, so the repository does not vendor DeepProve.

The paper and Notion plan use Gemma 3 270M Instruct. The official Hugging Face
repository may require an accepted license and token. This host's measured
run used the public `MatthewsFace/gemma-3-270m-it` mirror at sequence length 8
because `google/gemma-3-270m-it` returned HTTP 401. Treat that mirror as a
reproducibility fallback, not as the final model identity.

`mobile_diffusion_ezkl.py` loads the real MobileDiffusionTiny checkpoint,
runs and exports its 8x8 latent UNet, and asks EZKL to compile it. The current
EZKL release reaches its convolution scale assertion and writes a blocked
manifest. That result is intentional. An ONNX export or a proof of a single
toy block would not prove the full MobileDiffusion pipeline. The pending
relation includes text encoding, initial noise, scheduler, UNet, VAE decoder,
output serialization, output commitment, and VRF/HPKE binding.

## Commands

Activate the repository environment and keep the host within the agreed
budget. The examples use logical CPUs 0 through 15 and no GPU.

```bash
source .venv/bin/activate
OMP_NUM_THREADS=16 MKL_NUM_THREADS=16 OPENBLAS_NUM_THREADS=16 \
  taskset -c 0-15 python stage2/proof/ezkl_diffusion.py \
  --output-dir /tmp/poml-proof/diffusion-small
```

The command leaves `proof.json`, `settings.json`, `vk`, `srs`, and a
`manifest.json` in the output directory. The prover key and witness are
removed by default after proving. Add `--keep-prover-key` or `--keep-witness`
when investigating the run. Setup files are experiment artifacts and must not
be committed to Git.

The worker profile `diffusion-small-ezkl` invokes `ezkl_attempt.py` internally.
It keeps setup artifacts under the run directory's `proof-cache-*` folder and
removes each per-attempt witness after the proof response is returned.
For a multi-node devnet, prepare one cache and set `POML_EZKL_CACHE` before
calling `scripts/run_devnet.sh`. Every node then reads the same settings,
verification key and SRS that the profile digest pins.

The checked-in experiment command can create a complete cache. Keep its
prover key because the node needs it when a miner wins.

```bash
source .venv/bin/activate
OMP_NUM_THREADS=16 MKL_NUM_THREADS=16 OPENBLAS_NUM_THREADS=16 \
  taskset -c 0-15 python stage2/proof/ezkl_diffusion.py \
  --output-dir /tmp/poml-proof/diffusion-cache --keep-prover-key
POML_EZKL_CACHE=/tmp/poml-proof/diffusion-cache \
  ./scripts/run_devnet.sh run/diffusion-ezkl
```

For Gemma, use the official model when access is available.

```bash
POML_DEEPPROVE_MODEL=google/gemma-3-270m-it \
  POML_DEEPPROVE_OUTPUT=/tmp/poml-proof/gemma3-270m \
  stage2/proof/run_deepprove_gemma.sh
```

The measured public-mirror run can be reproduced with

```bash
POML_DEEPPROVE_MODEL=MatthewsFace/gemma-3-270m-it \
  POML_DEEPPROVE_OUTPUT=/tmp/poml-proof/gemma3-270m \
  stage2/proof/run_deepprove_gemma.sh
```

For the MobileDiffusion compatibility check, point the command at a local
Diffusers directory. The test checkpoint used during development was
`ikozlov/MobileDiffusionTiny`.

```bash
source .venv/bin/activate
OMP_NUM_THREADS=16 MKL_NUM_THREADS=16 OPENBLAS_NUM_THREADS=16 \
  taskset -c 0-15 python stage2/proof/mobile_diffusion_ezkl.py \
  --model-dir /path/to/MobileDiffusionTiny \
  --output-dir /tmp/poml-proof/mobile-diffusion \
  --local-only
```

## How this connects to the node

The `diffusion-small-ezkl` worker puts the model proof in a `PMLP` envelope,
passes its public instance to the auxiliary helper, and signs the combined
statement. A peer's Go validator first checks the bundle and certificate, then
spawns `verify_ezkl.py`. The process checks the EZKL proof with the configured
`settings.json`, verification key and SRS, and recomputes their pinned setup
digest. Configure a node with `proof_verifier_command`,
`proof_verifier_profiles_dir` and `proof_verifier_cache_dir` before using a
`zk-` profile. `scripts/run_devnet.sh` adds these settings automatically for
local devnets.

DeepProve and EZKL still do not put the PoML VRF and HPKE arithmetic inside one
model circuit. A later fully recursive construction can replace the composed
certificate with an outer proof while keeping the same public statement.

In simple terms, the small diffusion command and the Gemma command now prove
real model calculations. MobileDiffusion is measured and exported, but its
full proof is blocked by the current backend. The diffusion node now checks
the model proof and the auxiliary transcript together, while the two checks
still live in separate proof systems.

## Upstream references

- [DeepProve repository](https://github.com/Lagrange-Labs/deep-prove)
- [DeepProve zkML README](https://github.com/Lagrange-Labs/deep-prove/blob/master/zkml/README.md)
- [EZKL repository](https://github.com/zkonduit/ezkl)
- [MobileDiffusion paper](https://arxiv.org/abs/2311.16567)
