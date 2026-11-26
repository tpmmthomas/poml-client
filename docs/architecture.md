# Architecture guide

This document explains the client from the process boundary inward. The
protocol plan remains the source for paper-specific choices. The code keeps
the same boundary in small packages so a worker or transport can change
without moving consensus logic.

The implemented Stage 1 rules are summarized in [protocol.md](protocol.md).

## Processes

Each honest machine runs one `pomld` process. It connects to other nodes over
TCP and to one inference/certificate worker over a Unix socket. The user uses
`pomlctl` over local JSON-RPC. The experiment runner starts processes and reads
public RPC results and event files. It does not select a tip or inject private
attempt results.

```text
user / pomlctl
        |
        | JSON-RPC
        v
     pomld <--------------------> pomld
        |       protobuf TCP       |
        |                          |
        | protobuf Unix socket     |
        v                          v
   pomlworker                 model_worker.py
```

## Node lifecycle

`internal/app.Node` performs the wiring.

1. Load and hash genesis.
2. Validate the node's identity and both VRF seeds against its genesis miner.
3. Open the Pebble database and recover the stored tip, state, mempool and
   attempt counter.
4. Start the JSON-RPC server and authenticated P2P listener.
5. Start mining when the TOML file enables it.
6. Cancel the services, wait for peer and RPC goroutines, then close the
   miner, event ledger, chain and database in that order.

## Consensus path

`internal/codec` owns the accepted bytes. `internal/chain` checks a received
block against its parent state. It validates signatures, prefix and claim
digests, VRF output, ticket target, the configured certificate policy and state
transitions. The temporary random-proof policy is profile-visible and
intentionally weaker than the fixture signature.
Only after those checks does it store the block and state in Pebble.

The chain stores side branches. A valid block at a greater height becomes the
local canonical tip. An equal-height block wins only when its arrival time is
earlier than the current branch. A reorganization is represented by changing
the tip pointer. Historical block and state entries remain available for
inspection and synchronization.

## Two-phase mining

`internal/miner` receives the current tip and a snapshot of mempool contents.
It freezes ordinary transactions, computes `prefix_digest`, allocates a
persistent attempt ID, and dispatches independent worker requests. A completed
attempt produces one deterministic ciphertext and one ticket candidate.

The first valid locally observed hit stops new dispatches. Results already
delivered to the event loop become late response transactions. The miner then
requests one certificate, constructs the complete block and submits that block
to `chain.AddBlock`. The same validator checks a locally built block and a
remote block.

## Worker boundary

`internal/worker` owns the protobuf request and response framing. The fixture
worker hashes the supplied profile, query, attempt, input and public
randomness. Its certificate operation signs the complete public winner
statement with the genesis test key. The Python Stage 2 worker implements the
same request shape and returns deterministic dummy or model output. Its current
certificate operation emits random bytes only for the explicit development
profile. A future proof worker can replace that operation without changing the
Go node's model dispatch path.

The node never asks the worker for the current tip. Every request includes the
parent, prefix digest, query, attempt and prescribed randomness. A later model
worker can replace the fixture binary while retaining this request shape.

## P2P path

`internal/p2p` frames protobuf envelopes with a four-byte length prefix. Peers
authenticate a signed hello containing protocol version, genesis hash, node
identity and current tip. They exchange addresses, request missing canonical
blocks and relay transactions and complete blocks after local validation.

The node application owns duplicate suppression through the mempool and block
store. The P2P layer owns connection lifecycle, address discovery, bounded
frame size and write serialization.

## Experiment ledger

`internal/events.Writer` writes one JSON object per line. Every event receives
run ID, node ID, boot ID, genesis hash, local sequence, monotonic duration and
UTC wall time. Mining events also include query ID, attempt ID, ticket hash,
operation count, inference duration and ciphertext size. Consensus events
include parent, block hash, height, fork and rejection reasons.

Private inputs, model outputs before encryption, encryption VRF material,
private keys and worker witnesses stay out of the event ledger.

