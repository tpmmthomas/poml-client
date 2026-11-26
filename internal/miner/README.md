# Miner

[The lifecycle](miner.go) takes a snapshot by parent hash and freezes the exact
ordinary transaction list once, before inference. Peer adoption cancels
outstanding work by closing its worker requests. Starting or stopping the
miner joins the preceding scheduler.

[The local scheduler](round.go) picks uniformly among sorted eligible queries
for each durably numbered attempt. Its pseudorandom choice is reproducible
from that attempt ID, parent and prefix. Different miners can see a different
pool. When demand stops, the miner waits. The `max_attempts` checkpoint emits
an accounting event during a long loss streak and preserves its frozen
prefix. A disk-counter failure stops dispatch until the node can reserve a
new number.

[Attempt execution](attempt.go) supplies the complete prescribed inputs and
derives one canonical encrypted output. Every completed candidate gets a
ticket, and the local event loop seals on the first observed hit. A response
records its original parent, prefix, query, randomness and ciphertext. An
answer to a challenge can rerun the same attempt to reconstruct its private
witness without disclosing it to peers.

[Sealing](seal.go) appends observed losses in completion order. It reserves the
exact size of the two fixture signatures when applying the public limits. The
first response observed for a query is retained because the ledger allows one
pending claimant per query. A later local hit supersedes that response, and a
duplicate query result is recorded and discarded. Other eligible responses go
to the local durable response queue. The worker receives one certificate
request for the winner after the suffix is fixed. Local validation and peer
validation use the same chain entry point.

The ledger correlates `attempt_id`, `prefix_digest`, `ticket_hash` and
`completion_sequence` across worker timing, tickets, proof timing, overflow
and stale attempts. `response_create` links a signed transaction ID to its
attempt, byte size and assembly time. `C_service` and `C_cert` count signed
responses and certification calls respectively; `C_ticket` is the configured
lottery weight. Ordinary mining records use `purpose=mining`; challenge
replay uses `purpose=challenge`.

## Reading the mining ledger

| Question | Rows to use |
|---|---|
| How many lottery chances hit? | Count distinct `ticket_hash` among `ticket_hit`. A hit observed after a local seal can also have an `attempt_stale` row. |
| How much work went into tickets? | Join `attempt_start` and `worker_finish` by node, parent, attempt and `purpose=mining`. Sum actual worker wall time (`inference_ns`) or available `cpu_ns` samples. |
| What was interrupted? | `worker_cancel` reports elapsed request wall time, and `attempt_cancel` or `attempt_stale` identifies the affected attempt. Unreturned worker CPU time is unknown. |
| Which loss went into the mempool? | Join `response_create` to `response_queue` by transaction ID. The queue reason distinguishes block limits, work observed after sealing and interrupted rounds. |
| What did the proof service cost? | Join `proof_queue`, `proof_start` and `proof_finish` or `proof_failure` by request ID. `certification_ns` measures the worker; `wall_ns` also includes the call and local certificate verification. |
| Which hits became local blocks? | Join `seal`, `block_build` and `block_publish` by miner and ticket. The chain's adoption/reorg rows determine which branch ultimately counted. |

Count a delivered answer with a user-side decryption event and a confirmed
settlement with canonical ledger state. A computed loss, a published response
and a paid query measure different stages of service.
