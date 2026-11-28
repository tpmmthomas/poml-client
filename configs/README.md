# Configuration examples

`node.example.toml` shows the operator fields required by one node. Use
`pomlctl init-devnet` for complete generated configs because the node's private
keys must match the public keys committed in genesis. `certificate_socket` is
optional. Set it when a separate prover should receive certificate requests.

Models whose certificate profile starts with `zk-` also require the four
`proof_verifier_*` fields shown in `node.example.toml`. The command is usually
the repository virtualenv Python followed by `stage2/proof/verify_ezkl.py`.
The profiles directory, settings cache, verification key and SRS are consensus
inputs and must be provisioned identically on every peer. The
`diffusion-small-ezkl` profile pins their tagged SHA-256 setup digest in
`profiles/diffusion-small-ezkl.json`. A verifier computes that digest before
calling EZKL and rejects a cache from a different circuit build.
For a local multi-node run, set `POML_EZKL_CACHE` to one prepared cache and
let `scripts/run_devnet.sh` link each node to it.

