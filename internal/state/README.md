# State

This package applies signed transactions and winner settlement to a copied
parent state. It computes a deterministic state root by sorting every map key.
Height advances are the only clock used for claim deadlines.

`Response` locks the configured miner bond and creates one pending claim for a
query. Responses are a special service-claim transaction class, so a miner may
include several of them with the same ordinary account nonce. `Challenge` pays
a challenge fee into a height-bounded challenge window. `ChallengeAnswer`
requires the original response context and a proof statement, then pays the
query escrow once and releases the bond. An unanswered challenge refunds the
query and applies the configured bond slash. A pending claim that reaches its
deadline without a challenge settles automatically.

The query fee is escrowed when the query transaction is executed. Mempool
balance checks do not reserve funds, so conflicting queries race and the
deterministic block order decides which one succeeds. Registration deposits a
miner bond and records its identity and VRF public keys in state.

