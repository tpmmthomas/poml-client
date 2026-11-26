# Internal packages

The internal packages are the node implementation. Keep consensus decisions
in `chain`, `codec`, `consensus` and `state`. Keep process and transport
concerns in `app`, `p2p`, `rpc`, `store`, `events` and `worker`.

Changes to canonical bytes require new test vectors and a new protocol or
genesis version. Changes to worker implementation should preserve the worker
protobuf request and response shape whenever possible.

