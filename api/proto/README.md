# Worker schema

[poml.proto](poml.proto) is the source of truth for the private
inference/certification protocol. `poml.pb.go` is generated Go code.
Run `make proto` from the repository root after editing the schema.
A backend in another language can compile the same file with its protobuf
generator.

Worker protocol version 1 carries explicit model and profile digests. The
worker echoes request ID, version and profile digest. Reuse of a retired
field or enum number would make older programs interpret different data as
the same message. Reserve removed numbers and bump the worker version for
incompatible changes. The private witness belongs only on the worker socket.
