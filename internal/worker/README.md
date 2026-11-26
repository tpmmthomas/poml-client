# Worker process

The node opens one Unix connection per inference or certification job. A client
can set a separate certificate socket to plug a different prover into the same
node; the single-socket fixture serves both by default. A
4-byte big-endian length precedes the versioned protobuf envelope. Both sides
cap the frame size at 16 MiB. The fixture creates a socket with mode 0600.
Cancellation closes the connection and interrupts outstanding work.

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
  genesis-pinned test key signs the statement; independent nodes verify the
  signature. Use these timings to study scheduling, proof service overhead or
  injected delay. Model performance and ZKP performance require different
  workers.
- [The executable](../../cmd/pomlworker/main.go) accepts `--config` with the
  generated node configuration. Set `--inference-delay 50ms` or
  `--proof-delay 20ms` to exercise cancellation. Both default to zero.

[The Unix tests](worker_test.go) exercise both profiles on one server, incorrect
fields, a missing witness, response identity and canceling a delayed request.

[Process recovery testing](process_test.go) builds the actual worker command,
kills it mid-inference, restarts it using the same socket path and submits
both fixture profiles successfully. Run `go test -race ./internal/worker` to
exercise that path along with socket validation and cancellation.
