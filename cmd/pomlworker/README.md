# pomlworker

Start a fixture from the generated private node TOML with
`pomlworker --config /path/to/node-0.toml`. A standalone worker can instead
accept `--socket /path/to/private.sock --key-file /path/to/fixture-key.hex`.
Model and proof requests share the socket. Every call declares its profile and
resource bounds. See [the worker guide](../../internal/worker/README.md) for
latency controls and the wire contract.
