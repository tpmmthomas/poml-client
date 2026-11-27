# pomlproof

`pomlproof` is the small local bridge used by Python model workers for
auxiliary PoML profiles. It reads one protobuf `WorkerRequest` from standard
input, checks the private witness with the same Go RFC 9381 and HPKE code used
by the node, then writes an Ed25519 statement signature to standard output.

It does not create a zero-knowledge proof. A model proof can be returned by a
Python backend and carried separately in the proof bundle. The node still
checks the bundle digest and binding signature before accepting a block.

Example:

```bash
printf '%s' '<serialized WorkerRequest>' |
  ./pomlproof --certificate-private-key-hex "$POML_CERTIFICATE_KEY"
```

The devnet launcher builds this binary automatically and passes it to every
Python worker. Certificate keys are generated for the local run and must not
be copied into a repository.
