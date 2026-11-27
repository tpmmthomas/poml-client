# Worker process

The node opens one Unix connection per inference or certification job. A client
can set a separate certificate socket to plug a different prover into the same
node; the single-socket fixture serves both by default. A
4-byte big-endian length precedes the versioned protobuf envelope. Both sides
cap the frame size at 16 MiB. The fixture creates a socket with mode 0600.
Cancellation closes the connection and interrupts outstanding work.

The Go fixture is useful for Stage 1 protocol tests. Stage 2's
`stage2/model_worker.py` implements the same framed protobuf protocol in
Python. Its `dummy` backend is deterministic, its `diffusion-small` backend
runs the fixed PyTorch denoiser, and its `gemma` backend wraps a bounded
Hugging Face generation loop. The node selects one through the model profile
in genesis, so consensus code does not import a model library.

- [The client](worker.go) supplies the model and profile digests, signed query,
  verified input, parent, frozen prefix, request/attempt/miner IDs, resource
  bounds and ordered randomness. It checks the reply ID, profile, output
  size/shape, digest and evidence of execution.
- [The fixture server](fixture.go) accepts either shipped profile. The synthetic
  inference depends on the model, input and prescribed randomness. It returns
  canonical bytes, byte shape, operation count, execution witness and worker
  time. On Linux the worker also measures the serving thread’s CPU time for the
  computation. Simulated delays contribute only to wall time. Other platforms
  leave the CPU sample absent. The node encrypts the result.
- [The certificate request](../../api/proto/poml.proto) carries purpose, public
  statement, private execution/output/encryption witness and budgets. A
  genesis-pinned key signs the statement; independent nodes verify the
  signature. Auxiliary profiles route the request through `pomlproof`, which
  verifies the input opening, inference VRF, expanded randomness, encryption
  VRF, hidden output commitment and deterministic HPKE ciphertext before the
  signature is made. A proof-capable Python worker can also return model proof
  bytes in the same response. Use these timings to study scheduling, proof
  service overhead or injected delay. Model performance and ZKP performance
  require different workers.
- [The executable](../../cmd/pomlworker/main.go) accepts `--config` with the
  generated node configuration. Set `--inference-delay 50ms` or
  `--proof-delay 20ms` to exercise cancellation. Both default to zero.

[The Unix tests](worker_test.go) exercise both profiles on one server, incorrect
fields, a missing witness, response identity and canceling a delayed request.

[Process recovery testing](process_test.go) builds the actual worker command,
kills it mid-inference, restarts it using the same socket path and submits
both fixture profiles successfully. Run `go test -race ./internal/worker` to
exercise that path along with socket validation and cancellation.

For Python worker checks, run `taskset -c 0-15 .venv/bin/python
stage2/test_model_worker.py`. The Gemma test is skipped unless
`POML_GEMMA_MODEL` points to a local or accessible checkpoint. The temporary
`dummy-random-v1` certificate is deliberately accepted only by the matching
profile and is not a model proof.
