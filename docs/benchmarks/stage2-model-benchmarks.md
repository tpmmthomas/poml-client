# Stage 2 model worker benchmark

**Run date:** 26 November 2026

**Host:** AMD Threadripper Pro 5955WX, 32 logical CPUs, 125 GiB RAM, three
NVIDIA RTX A6000 devices with 49 GiB each

**Resource rule:** every run was pinned to logical CPUs 0–15. That is half of
the host's logical CPUs. GPU runs used `CUDA_VISIBLE_DEVICES=0` and
`cuda:0`. No benchmark used another GPU.

The benchmark command was

```bash
taskset -c 0-15 .venv/bin/python scripts/benchmark_stage2.py \
  --profile <profile> --device <cpu-or-cuda:0> --threads 16 \
  --requests 8 --output /tmp/stage2-benchmark.json
```

The reported request time is the warm inference call. Model load time and
resident memory after loading are reported separately. The GPU numbers are
PyTorch allocator values. They do not include every driver allocation shown by
`nvidia-smi`.

## Results

| Profile | Device | Requests | Load | Median request | P95 request | RSS after load | GPU peak allocated |
|---|---|---:|---:|---:|---:|---:|---:|
| `dummy` | CPU, 1 thread | 8 | 0.06 ms | 0.026 ms | 0.036 ms | 22.5 MiB | 0 |
| `diffusion-small` | CPU, 16 threads | 8 | 1.58 s | 0.851 ms | 0.965 ms | 659.1 MiB | 0 |
| `diffusion-small` | `cuda:0`, 16 host threads | 8 | 1.80 s | 1.475 ms | 1.548 ms | 1016.5 MiB | 8.2 MiB |
| tiny Gemma adapter | CPU, 16 threads | 2 | 2.43 s | 1.181 ms | 1.211 ms | 712.7 MiB | 0 |
| tiny Gemma adapter | `cuda:0`, 16 host threads | 2 | 2.62 s | 28.58 ms | 28.59 ms | 1.45 GiB | 8.3 MiB |

The diffusion profile is a fixed 8x8 single-channel, four-step denoiser with
64 output bytes. The Gemma adapter benchmark used a locally generated two-layer
Gemma-compatible checkpoint with a 64-wide hidden state. It checks the adapter
and socket contract, not the resource cost of the real 1B checkpoint.

The diffusion reference vector matched on CPU and `cuda:0`. Gemma is measured
on both devices as an adapter performance check, but its CPU fp32 and GPU fp16
sampling paths can produce different token bytes. A consensus experiment must
pin one Gemma device and precision in its profile until a cross-device
canonical sampler and weight manifest are frozen.

## Gemma checkpoint status

The default profile names `google/gemma-3-1b-it`. Hugging Face returned HTTP 401
because the host has no authorization for that gated repository. A full Gemma
benchmark is therefore pending an approved Hugging Face token and license
acceptance. The worker already accepts a local directory or model ID through
`--model-ref`.

After access is available, run one node so only one process owns the GPU.

```bash
export HF_TOKEN=<token-with-access-to-the-approved-checkpoint>
taskset -c 0-15 .venv/bin/python scripts/benchmark_stage2.py \
  --profile gemma3-1b --model-ref google/gemma-3-1b-it \
  --device cuda:0 --threads 16 --requests 2 \
  --output /tmp/gemma3-1b-benchmark.json
```

The model revision and a weight manifest digest must be frozen in the profile
before treating that run as consensus or paper evidence. The current profile
digest identifies the Stage 2 adapter profile. It does not yet attest to a
downloaded Gemma weight manifest because the gated checkpoint could not be
retrieved on this host.

## How to run each profile through the node

The devnet generator selects the model in genesis. The worker launcher reads
that profile and starts either the Go fixture worker or the Python worker.

### Dummy model and dummy proof

```bash
export PATH=/home/thomas/.local/toolchains/go1.27.4/bin:$PATH
go run ./cmd/pomlctl init-devnet --dir run/dummy --nodes 3 --model dummy
POML_DEVICE=cpu ./scripts/run_devnet.sh run/dummy
go run ./cmd/pomlctl submit-query --node 127.0.0.1:19100 \
  --user run/dummy/user.json --model dummy --input "hello dummy"
python3 scripts/collect_events.py run/dummy --output run/dummy/summary.json
./scripts/stop_devnet.sh run/dummy
```

The dummy model is deterministic. The dummy proof is 64 random bytes. The Go
validator accepts a non-empty proof only when genesis selects
`dummy-random-v1`. This is useful for testing networking, tickets, encryption,
block propagation, and resource logging. It provides zero soundness.

### Small diffusion model

```bash
go run ./cmd/pomlctl init-devnet --dir run/diffusion --nodes 1 --model diffusion-small
POML_DEVICE=cpu ./scripts/run_devnet.sh run/diffusion
go run ./cmd/pomlctl submit-query --node 127.0.0.1:19100 \
  --user run/diffusion/user.json --model diffusion-small \
  --input "a small denoising request"
```

Set `POML_DEVICE=cuda:0` to use the first GPU. The launcher rejects GPU IDs
other than `cuda:0` and prevents multi-node Gemma GPU runs. The worker uses the
paper-style initial noise plus one independent noise slot per denoising step.

### Gemma

After the checkpoint is available, use one node for a first run.

```bash
go run ./cmd/pomlctl init-devnet --dir run/gemma --nodes 1 --model gemma3-1b
POML_DEVICE=cuda:0 ./scripts/run_devnet.sh run/gemma
go run ./cmd/pomlctl submit-query --node 127.0.0.1:19100 \
  --user run/gemma/user.json --model gemma3-1b \
  --input "Explain verifiable machine learning in one sentence."
```

The canonical Gemma output is a big-endian sequence of 32-bit token IDs. The
current CLI decrypt command prints the ciphertext payload as hexadecimal.

## What is different from the final paper plan

The implementation is deliberately useful before the final ZKP system exists.

- The dummy proof accepts random bytes. The final plan requires a proof of the
  model, VRF, encryption, output commitment, and exact block context.
- The small diffusion network is a real deterministic PyTorch denoiser, but it
  is much smaller than Stable Diffusion. Its 8x8 output and four steps are a
  protocol test profile.
- The Gemma adapter runs Hugging Face causal generation with fixed context and
  token limits. The real Gemma weights still need authorized download and a
  frozen manifest digest.
- Stage 1 still encrypts `output || query_id` and carries the old certificate
  field. The output-commitment nonce, composite proof bundle, and recursive
  proof verifier from the final Stage 2 plan are not yet in the block wire
  format.
- Go still performs the real RFC 9381 VRF and deterministic RFC 9180 HPKE work.
  The Python worker supplies model output and the temporary proof bytes.

In simple terms, the node can now run the real model-shaped work and carry its
result through the same distributed chain. It can measure that work and test
the cryptographic plumbing. It cannot yet prove that a model really produced
the encrypted answer.
