# Worker profiles

A model profile tells every node which model digest, input randomness layout,
maximum output size, public ticket weight, runtime budget and certificate
profile the experiment shares. The node hashes those fields in a fixed order
and checks the worker echoes the same digest on each response. Changing a
profile changes the chain's configuration.

| Local test profile | Randomness slots | Output | Ticket weight |
|---|---:|---:|---:|
| [fixture-a](fixture-a.json) | One 32-byte slot | 32 bytes | 1 |
| [fixture-b](fixture-b.json) | Three 32-byte slots | 64 bytes | 2 |
| [dummy](dummy.json) | One 32-byte slot | 32 bytes | 1 |
| [diffusion-small](diffusion-small.json) | Initial noise plus four step slots | 64 bytes | 1 |
| [diffusion-small-aux](diffusion-small-aux.json) | Initial noise plus four step slots | 64 bytes | 1 |
| [diffusion-small-ezkl](diffusion-small-ezkl.json) | Initial noise plus four step slots | 64 bytes | 1 |
| [gemma3-1b](gemma3-1b.json) | One 32-byte sampling slot | Up to 16 token IDs | 1 |

The two fixture profiles are synthetic hash functions implemented in [the
fixture worker](../internal/worker/fixture.go).
The model digests identify the synthetic functions. Ticket weights are fixed
experimental inputs. The wall time and operation count reported by a run
measure the fixture's behavior. For a different model, keep the socket
contract, supply its model profile in genesis and run a worker that
implements that profile. The scheduler accepts arbitrary validated profiles.

The Stage 2 profiles select the Python worker. `dummy` is deterministic model
output plus a 64-byte random certificate accepted only by the
`dummy-random-v1` profile. `diffusion-small` is a deterministic 8x8 PyTorch
denoiser with four steps. `diffusion-small-aux` uses the same model with the
full host-side PoML transcript checker. `diffusion-small-ezkl` evaluates the
fixed-point EZKL circuit before ticket construction and returns an EZKL proof
after its setup cache is ready. Its `proof_setup_digest` pins `settings.json`,
`vk` and `srs` for peers. `gemma3-1b` uses the Hugging Face
`google/gemma-3-1b-it` adapter, bounded to 16 generated tokens. Its profile
digest identifies the adapter contract, and a downloaded weight manifest still
needs to be frozen before a consensus or paper run.

The random certificate is a temporary development mode. It tests the node,
ticket, encryption and relay plumbing but does not prove that a model produced
the answer. The auxiliary profile checks the private transcript before issuing
its binding certificate. See [the Stage 2 proof status](../docs/stage2-proof-status.md)
for the composed path and the remaining single-relation and Gemma consensus work.
