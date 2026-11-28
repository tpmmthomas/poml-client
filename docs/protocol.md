# Stage 1 protocol boundary

This file records the consensus-facing choices implemented by the client. The
paper change plan remains the detailed design record. A change to any item
below changes the bytes that nodes agree on and needs new vectors and tests.

## Block construction

Every block has an ordered **prefix** and a late **response suffix**.

- The prefix contains the ordinary state-changing transactions available when
  the miner starts inference. The miner freezes it and commits its digest.
- The prefix digest is included in the inference attempt input and ticket
  bytes. Model output and the late suffix are excluded from that input.
- The miner runs independent attempts against eligible queries. The first valid
  hit observed by its local event loop seals the round.
- Completed losing answers observed before the seal become response
  transactions. Only one response for a query can be retained in a round,
  because the ledger permits one pending claimant per query. A winning answer
  supersedes an earlier local losing response for the same query.
- A legacy fixture winner carries one certificate. An auxiliary winner carries
  a versioned proof bundle whose binding certificate covers the winner, the
  frozen prefix digest and the complete response-suffix digest. The bundle also
  carries the model proof bytes and their setup/profile digest.
- A response transaction is a special service claim. It uses its signed query
  and attempt context for identity and does not consume an ordinary account
  nonce. This lets one miner answer several queries in one block.

The public response count and block-byte limits are read from genesis. A
response that does not fit is retained in the local response queue with an
event record. The current devnet uses a deliberately large response limit.

## State and deadlines

Query fees enter escrow when a query transaction executes. Checking a query in
the mempool does not reserve its balance. Responses lock the configured miner
bond. A challenge and challenge answer use ordinary account nonces. Challenge
deadlines advance only when a block height advances, so a halted chain also
halts deadline progress.

An unanswered optimistic claim refunds the query and applies the configured
slash. A valid answer pays the escrow once and releases the bond. A proof-based
winner pays the escrow directly and cannot coexist with an optimistic claim for
the same query on that branch.

## Fork choice

Nodes retain valid side branches and select the greatest height. For equal
heights, the first valid branch observed by that node wins. A later longer
branch causes a reorganization. The node restores transactions from detached
canonical blocks to its mempool and removes transactions on the newly adopted
path.

The same block validator handles local proposals and peer-delivered blocks.
Duplicate ticket bodies are valid competing children and count as one lottery
opportunity per attempt. The event ledger records forks, reorganizations and
duplicate response observations.

## Cryptographic profile

- Tagged SHA-256 identifies protocol objects and commitments.
- Ed25519 signs accounts, miners, blocks and Stage 1 fixture certificates.
- ECVRF-EDWARDS25519-SHA512-ELL2 from RFC 9381 supplies inference and
  encryption randomness with separate miner keys.
- User output uses RFC 9180 Base HPKE with X25519, HKDF-SHA256 and AES-256-GCM.
  The encryption VRF output deterministically supplies the ephemeral key.

Auxiliary profiles derive a hidden 32-byte output-commitment nonce from the
private encryption-VRF output. The encrypted payload carries that nonce so the
user can recompute the tagged SHA-256 output commitment.

The exact Go dependencies and versions are pinned in `go.mod`. The
`aux-ed25519-v1` certificate is a transcript-checking integration profile. It
proves that the local prover checked the worker witness, both VRFs, the output
commitment and deterministic HPKE ciphertext before signing. It is not itself a
zero-knowledge proof. The `zk-ezkl-diffusion-v1` profile carries a real EZKL
model proof in the same envelope. Its `PMLP` model-proof envelope exposes the
canonical EZKL public instance, and the auxiliary certificate signs that same
instance. Every configured peer invokes `stage2/proof/verify_ezkl.py` before
accepting the block.

The two proof layers are therefore composed through a shared public instance.
The model proof establishes the diffusion computation for that instance. The
auxiliary certificate establishes the VRF, commitment, HPKE and block-context
transcript for the same instance. This is independently checkable by peers,
but it is still a composed proof. For the EZKL profile, the worker obtains the
canonical output bytes from the circuit witness before it computes the ticket.
The current EZKL circuit does not yet place
SHA-256, ECVRF and HPKE arithmetic inside the EZKL circuit itself.

## Replaceable worker boundary

The private Unix-socket protobuf protocol supplies the model profile, signed
query, input, parent, prefix digest, attempt ID, ordered randomness, resource
limits and private witness. A later inference or proving process can replace
the fixture worker without changing peer transport or consensus code.
