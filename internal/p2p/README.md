# Peer networking

Each daemon runs one TCP listener. Give the first node a reachable `advertise`
address and use it as a seed for another node. Peers exchange addresses after
signing a fresh challenge. A node saves learned addresses to its local address
book and can reconnect after its original seed shuts down.

## What happens on a connection

1. Both ends exchange their name, public key, listening address, network
   version, genesis hash, chain and profile. Each signs the two Hello messages
   in order with Ed25519. The public key identifies the peer. Its name is a
   display label.
2. Peers exchange known addresses, chain tips and mempool inventories. An
   inventory entry contains an object type and an ID. A node requests bytes
   for missing objects, recomputes the ID and runs its own validator. A
   successful admission triggers an announcement to other peers.
3. To catch up, a node sends points from its current chain, ending with
   genesis. The serving peer finds their most recent shared point and returns
   pages of at most 16 block IDs, heights and parents. The receiver requests
   each complete block and passes it through the same admission path as live
   gossip. A new child announced out of order triggers a request for its
   parent. Fork choice belongs to the chain package.
4. Query input can travel separately. The node requests a commitment, checks
   the returned bytes against the signed digest and stores them locally.
   Receiving an input announcement by itself does not start a download.
5. A heartbeat exchanges current tips and records round-trip time on one
   monotonic clock. A timeout or broken socket triggers another dial with
   exponential backoff. A reconnect repeats synchronization and downloads
   missing mempool transactions.

## Source map

| Files | Responsibility |
| --- | --- |
| `p2p.go`, `discovery.go` | Listener, connection limits, persisted addresses and reconnect backoff. |
| `handshake.go`, `session.go`, `transport.go` | Identity, socket lifetime, heartbeat, byte framing and bounded sends. |
| `relay.go`, `inventory.go`, `objects.go` | Object announcement, mempool pages, input retrieval, admission and deduplication. |
| `sync.go` | Block locators, ancestor lookups, header pages and full-body verification. |
| `wire/` | Versioned protobuf frame and message definitions. |

The node supplies callbacks for its mempool, branch-aware block store, input
cache, public validator and event writer. The peer manager can use cheap
store-membership checks. Downloads and sends run concurrently. A per-node
admission lock ensures that two peers offering the same ID trigger one local
validation. The node announces a new local block after
the durable commit. Sync receives ordinary public bytes. Mining can stay off
throughout sync.

## Bounds and measurements

The experimental client allows 32 sockets and 128 learned addresses per node.
A peer gets two object requests in flight, a 64-frame send queue with a byte
budget, and eight seconds per outstanding request. The input and block byte
limit comes from genesis, up to the transport ceiling of 16 MiB. Control
payloads are capped at 64 KiB. Limits and heartbeat intervals live in the
networking package. Their values affect measured propagation under load.

`peer_connected` and `peer_disconnected` describe the observed topology.
`peer_rtt` measures a complete round trip on one node. `block_arrival` records
a requested full body reaching the reader before validation or disk access.
`block_validate` records the local result
and elapsed time. `tx_validate`, `input_fetch` and `input_validate` describe
the other data paths. Experiment logs include IDs, lengths and timing. Keep
each machine's clock offset in the run manifest when comparing wall-clock
timestamps from different hosts.

Run `go test -race ./internal/p2p/...` to exercise real loopback TCP sockets,
a multi-page fork recovery, reconnect from disk, relay deduplication, input
validation and paged mempool catch-up. These package tests supply an
in-memory ledger to isolate transport. The repository's process simulation
uses each daemon's actual disk and consensus validator.
