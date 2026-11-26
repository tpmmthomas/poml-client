# Scripts

`bootstrap.sh` installs the pinned Go and Protocol Buffer tools under a local
prefix. It also creates the repository `.venv` and installs the pinned Python
model-worker dependencies. Set `POML_SKIP_PYTHON=1` to skip that part.

`run_devnet.sh` builds binaries, starts one worker and node per generated
config, and waits until every worker socket and RPC endpoint is ready. It reads
the model profile from genesis. Fixture profiles use the Go worker. `dummy`,
`diffusion-small` and `gemma3-1b` use `stage2/model_worker.py`. The launcher
pins every child process to the first half of the host's logical CPUs. Set
`POML_DEVICE=cpu` or `POML_DEVICE=cuda:0`; GPU mode exposes only the first GPU.
Gemma GPU runs are limited to one node so the single GPU is not shared by
several model processes.
Set `POML_MODEL_REF` to a local checkpoint when a profile's default Hugging
Face reference is unavailable. This override is for adapter testing. Freeze a
real weight-manifest digest before using it as a consensus model profile.

`stop_devnet.sh` stops those processes and waits for them to exit.
`smoke_test.sh` creates a three-node fixture run, submits two queries, checks
height-2 convergence and decrypts the winning output. `benchmark_stage2.py`
loads one Stage 2 profile, performs warm requests, and writes timing, CPU,
resident-memory and GPU allocator measurements. `collect_events.py` turns
JSONL ledgers into a compact experiment summary while preserving the raw
files.

