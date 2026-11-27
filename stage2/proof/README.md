# Real model-proof experiments

This directory contains opt-in proving commands. The normal node still uses
the Stage 1 temporary certificate because a proof setup is too expensive to
run inside every inference request.

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
returns one proof JSON object for the worker's versioned proof bundle. Setup
artifacts are cached because setup is much slower than an individual proof.

The final PoML proof must connect this Poseidon commitment to the protocol's
tagged SHA-256 output commitment, deterministic HPKE ciphertext and VRF
transcript. The Go auxiliary profile now checks that transcript before signing
the bundle. A trusted peer-side EZKL verifier is still needed before the
`zk-ezkl-diffusion-v1` profile should be used against adversarial peers.

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

The proof commands deliberately stop before changing consensus. A complete
worker certificate needs a versioned proof bundle containing the model proof,
the public statement digest, and the binding proof. The binding relation must
show that the same hidden output produced the output commitment and HPKE
ciphertext, and that the encryption VRF and block context match. DeepProve and
EZKL do not supply that PoML-specific relation. The next integration step is a
small Rust binding guest or a custom circuit, followed by a protobuf proof
bundle and a fresh-node verifier test.

In simple terms, the small diffusion command and the Gemma command now prove
real model calculations. MobileDiffusion is measured and exported, but its
full proof is blocked by the current backend. None of these commands yet
proves the whole paper equation in one object.

## Upstream references

- [DeepProve repository](https://github.com/Lagrange-Labs/deep-prove)
- [DeepProve zkML README](https://github.com/Lagrange-Labs/deep-prove/blob/master/zkml/README.md)
- [EZKL repository](https://github.com/zkonduit/ezkl)
- [MobileDiffusion paper](https://arxiv.org/abs/2311.16567)
