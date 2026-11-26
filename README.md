# PoML Stage 1 node

`poml-node` is a fresh Go implementation of the distributed Stage 1 PoML
client. Every node process owns its key material, local database, mempool,
peer connections, worker connection, mining loop and event ledger. The test
network uses a deterministic fixture worker. A later model or proof worker can
use the same protobuf boundary.

Stage 1 measures node, network, storage, cryptographic plumbing and ledger
behaviour. The certificate returned by the fixture worker is an Ed25519 test
certificate. It exercises the future proof boundary and carries no claim about
model correctness or zero knowledge.

## Quick start

The repository expects Go 1.27.4. Go 1.26 is the minimum version because the
standard `crypto/hpke` package first appears there. The bootstrap script keeps
the toolchain and Protocol Buffer compiler in a repository-local `.tools`
directory.

```bash
POML_TOOL_PREFIX="$PWD/.tools" ./scripts/bootstrap.sh
export PATH="$PWD/.tools/go/bin:$PWD/.tools/protoc/bin:$PWD/.tools/bin:$PATH"
go test ./...
go build ./cmd/...
```

Create a three-node local network. The command writes independent node keys,
the common genesis file, worker socket paths, user keys and a manifest.

```bash
go run ./cmd/pomlctl init-devnet --dir run/devnet --nodes 3
./scripts/run_devnet.sh run/devnet
```

Submit a signed query through node 0. The query is relayed to the other nodes,
and any miner can select it after its ordinary transaction prefix is frozen.

```bash
go run ./cmd/pomlctl submit-query \
  --node 127.0.0.1:19100 \
  --user run/devnet/user.json \
  --input "hello from the user"
```

Inspect convergence and the event ledgers.

```bash
go run ./cmd/pomlctl get-tip --node 127.0.0.1:19100
go run ./cmd/pomlctl node-info --node 127.0.0.1:19101
python3 scripts/collect_events.py run/devnet
./scripts/stop_devnet.sh run/devnet
```

The `get-tip` result is a height and block hash. The event collector writes a
small JSON summary and leaves every node's append-only JSONL ledger in place.
The winning ciphertext can be decrypted by the user key holder after obtaining
the block hash.

```bash
go run ./cmd/pomlctl get-block --node 127.0.0.1:19100 --hash <block-hash>
go run ./cmd/pomlctl decrypt \
  --node 127.0.0.1:19100 \
  --user run/devnet/user.json \
  --hash <block-hash>
```

The decrypt command prints the fixture output as hexadecimal bytes.

## What happens in one round

1. A node receives a signed query and stores it in its mempool.
2. A miner selects ordinary transactions and freezes their ordered prefix.
3. The prefix digest becomes part of every attempt's inference-VRF input.
4. The node dispatches independent attempts to the worker process.
5. The node derives deterministic encryption coins from the encryption VRF,
   computes a canonical HPKE ciphertext and checks the fixed ticket target.
6. The first valid hit observed by that miner seals the round.
7. Completed losing attempts become response transactions in the late suffix.
8. The worker signs the complete winner statement with the Stage 1 fixture key.
9. The node validates the complete block through the same path used for peers,
   stores it in Pebble and relays it.
10. Each peer stores every valid branch and selects the longest valid chain.
    An equal-height tie uses the first valid branch observed locally.

Deadlines use block height. A stopped chain does not advance a challenge
deadline. The node can continue mining while a claim is pending.

## Components

The top-level layout is described in [the component guide](docs/architecture.md).
The consensus-facing Stage 1 rules are summarized in [the protocol boundary](docs/protocol.md).
Each implementation area has a short local README.

| Path | Responsibility |
|---|---|
| `cmd/pomld` | Long-running node daemon. |
| `cmd/pomlctl` | Devnet creation, signed query submission, RPC inspection and decryption. |
| `cmd/pomlworker` | Separate deterministic inference and certificate fixture. |
| `api/proto` | Versioned protobuf wire and worker messages. |
| `internal/chain` | Block validation, branch storage and fork choice. |
| `internal/codec` | Canonical transaction, block, ticket and statement bytes. |
| `internal/consensus` | Exact integer ticket target and boundary checks. |
| `internal/crypto` | Ed25519, RFC 9381 VRF and deterministic RFC 9180 HPKE profile. |
| `internal/events` | Append-only JSONL experiment ledger. |
| `internal/miner` | Prefix freeze, independent attempts, first-hit sealing and suffix assembly. |
| `internal/p2p` | Authenticated TCP peers, discovery, relay and block sync. |
| `internal/profile` | Genesis and TOML node configuration. |
| `internal/rpc` | Local JSON-RPC operator interface. |
| `internal/state` | Deterministic balances, queries, claims and settlement transitions. |
| `internal/store` | Pebble persistence for blocks, states, mempool and attempt counters. |
| `internal/worker` | Replaceable inference and certificate process boundary. |

## Cryptographic profile

The implementation uses tagged SHA-256 for identifiers and commitments,
Ed25519 for identity and fixture certificates, and
ECVRF-EDWARDS25519-SHA512-ELL2 from RFC 9381 for inference and encryption
randomness. The exact Go package is pinned as
`github.com/tmc/vrf/rfc9381 v0.1.0`.

User encryption follows RFC 9180 Base mode with DHKEM(X25519, HKDF-SHA256),
HKDF-SHA256 and AES-256-GCM. Go's standard `crypto/hpke` API supplies the RFC
reference primitives. PoML's prescribed-coin requirement needs a small
deterministic encapsulation adapter over `crypto/ecdh`, `crypto/hkdf`,
`crypto/aes` and `crypto/cipher`. Its round-trip and determinism checks are in
`internal/crypto/crypto_test.go`.

## Configuration

Consensus values live in genesis. Operator values live in TOML. The devnet
generator is the easiest way to obtain a complete, internally consistent set
of files. The generated manifest contains:

- `genesis.json` with the common chain ID, base target, model profile, keys and
  limits.
- `node-*.toml` with independent node and miner keys, listen addresses, RPC
  addresses, worker sockets and data directories.
- `user.json` with the user's signing and X25519 decryption keys.
- `manifest.json` with paths needed by scripts and experiment collectors.

The generated private keys are for local experiments. Keep them out of a
shared repository and use fresh keys for every run.

## Testing

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/...
./scripts/smoke_test.sh /tmp/poml-stage1-smoke
```

The tests cover canonical round trips, ticket threshold boundaries, VRF
verification, deterministic encryption and authenticated worker calls. The
multi-process smoke run is the `scripts/run_devnet.sh` workflow. It exercises
real loopback TCP and Unix worker sockets, independent Pebble directories,
query relay, competing blocks, first-seen fork handling, event logging and
user decryption.

## Current boundary

The Stage 1 implementation keeps real inference and ZKP generation behind the
worker protocol. The fixture worker makes output and certificate timing
repeatable so experiments can measure the client itself. Model-specific output
relations, production proof systems, proof soundness and paper security claims
remain later work.

