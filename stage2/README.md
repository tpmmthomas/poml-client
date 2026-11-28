# Stage 2 model worker

`model_worker.py` is the Python side of the existing Go worker protocol. The
Go node still owns consensus, VRFs, deterministic HPKE encryption, ticket
selection and block validation. This process receives a validated model
request and returns canonical output bytes, a binding certificate, and an
optional model proof object. For the EZKL profile, output bytes come from the
same fixed-point circuit that is proved, which removes the earlier mismatch
between ordinary model execution and proof output.

## Select a backend

The genesis profile selects the backend for every node in a run.

```bash
go run ./cmd/pomlctl init-devnet --dir run/dummy --nodes 1 --model dummy
go run ./cmd/pomlctl init-devnet --dir run/diffusion --nodes 1 --model diffusion-small
go run ./cmd/pomlctl init-devnet --dir run/diffusion-aux --nodes 1 --model diffusion-small-aux
go run ./cmd/pomlctl init-devnet --dir run/diffusion-ezkl --nodes 1 --model diffusion-small-ezkl
go run ./cmd/pomlctl init-devnet --dir run/gemma --nodes 1 --model gemma3-1b
```

Start the selected run with `./scripts/run_devnet.sh run/<name>`. The launcher
keeps every child on the first half of the host CPUs. Set `POML_DEVICE=cpu` or
`POML_DEVICE=cuda:0`. GPU mode exposes only the first GPU. A Gemma GPU run is
limited to one node so the model process owns that device.

For a local checkpoint in a devnet, set `POML_MODEL_REF=/path/to/checkpoint`
before `run_devnet.sh`. This is useful for adapter tests such as the tiny local
Gemma-compatible checkpoint used in the repository benchmarks. The profile's
current digest still describes the adapter contract, so this override must not
be used as final consensus evidence.

The `dummy` backend needs no downloaded model. It hashes the input and the
prescribed randomness into deterministic output bytes. Its certificate is 64
random bytes and is accepted only by `dummy-random-v1`, which is intentionally
insecure.

`diffusion-small` is a fixed 8x8 single-channel PyTorch denoiser. The profile
allocates one initial-noise slot and one slot for each of four denoising steps.
The output is 64 quantized bytes. The network weights are seeded from the
profile digest, so a clean install recreates the same reference model.

`diffusion-small-aux` uses the same model and routes proof requests through the
Go `pomlproof` helper. The helper checks the private PoML transcript before
signing the proof envelope. `diffusion-small-ezkl` evaluates the compiled EZKL
circuit for each attempt, then runs `ezkl_attempt.py` for a winning proof and
puts its verified EZKL proof JSON in the envelope. Its first setup can consume
several minutes and several gigabytes of temporary prover-key storage on this
host.

`gemma3-1b` uses Hugging Face `google/gemma-3-1b-it`, a 512-token input limit
and at most 16 sampled output tokens. The canonical output is big-endian
32-bit token IDs. Hugging Face access to Gemma may require accepting the model
license and setting `HF_TOKEN`. A local checkpoint can be used directly with
the worker's `--model-ref` option or the benchmark script.

The small diffusion vector is stable across the tested CPU and `cuda:0` paths.
Gemma currently uses CPU fp32 or GPU fp16 and sampled generation, so those two
execution paths can produce different token bytes. Pin one device and
precision before treating a Gemma run as consensus evidence.

## Run the worker directly

The normal entry point is `run_devnet.sh`. For adapter development, start the
worker directly after activating the repository environment.

```bash
taskset -c 0-15 .venv/bin/python stage2/model_worker.py \
  --socket /tmp/poml-stage2.sock \
  --profile-id diffusion-small \
  --profiles-dir profiles \
  --device cpu --threads 16 --cpu-start 0
```

The worker socket is mode `0600`. Its framed protobuf messages are defined in
`api/proto/poml.proto`. The checked-in `api/proto/poml_pb2.py` is the matching
Python generation.

## Run real model-proof experiments

The proving commands live in [proof/README.md](proof/README.md). They are
separate from the request worker because setup and proving are much slower
than inference.

The small diffusion command exports the same four-step denoising core used by
the worker, generates a real EZKL proof, verifies it and rejects a tampered
public output commitment. The output bytes stay private in that proof.

```bash
source .venv/bin/activate
OMP_NUM_THREADS=16 MKL_NUM_THREADS=16 OPENBLAS_NUM_THREADS=16 \
  taskset -c 0-15 python stage2/proof/ezkl_diffusion.py \
  --output-dir /tmp/poml-proof/diffusion-small
```

The Gemma command runs DeepProve's Gemma 3 SafeTensors path. The paper target
is Gemma 3 270M. An approved Hugging Face token is required for the official
checkpoint. The public mirror used for the local benchmark is documented in
the proof README.

```bash
POML_DEEPPROVE_MODEL=google/gemma-3-270m-it \
  stage2/proof/run_deepprove_gemma.sh
```

The MobileDiffusion command exports the real UNet and records the current EZKL
compiler limitation. It does not label an export as a proof.

## Tests and measurements

```bash
taskset -c 0-15 .venv/bin/python stage2/test_model_worker.py
taskset -c 0-15 .venv/bin/python scripts/benchmark_stage2.py \
  --profile diffusion-small --device cpu --threads 16 --requests 8 \
  --output /tmp/diffusion-small.json
```

The Gemma unit test runs only when `POML_GEMMA_MODEL` names an accessible
checkpoint. The benchmark report in
`docs/benchmarks/stage2-model-benchmarks.md` records the host, CPU cap,
memory, timing and the authorized-checkpoint limitation.

## Boundary with the final paper

The `aux-ed25519-v1` profile wires the output commitment, VRF, HPKE and
block-context binding relation into the Go validator. The
`zk-ezkl-diffusion-v1` profile also carries a real model proof, and every
configured peer invokes the Python EZKL verifier before accepting the block.
The profile is a composed model proof plus transcript certificate. A single
zero-knowledge relation that contains the auxiliary SHA-256, ECVRF and HPKE
arithmetic remains future work.
