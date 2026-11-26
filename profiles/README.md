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

Both are synthetic hash functions implemented in [the fixture worker](../internal/worker/fixture.go).
The model digests identify the synthetic functions. Ticket weights are fixed
experimental inputs. The wall time and operation count reported by a run
measure the fixture's behavior. For a different model, keep the socket
contract, supply its model profile in genesis and run a worker that
implements that profile. The scheduler accepts arbitrary validated profiles.
