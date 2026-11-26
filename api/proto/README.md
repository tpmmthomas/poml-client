# Worker schema

[poml.proto](poml.proto) is the source of truth for the private
inference/certification protocol. `poml.pb.go` is generated Go code.
Run `make proto` from the repository root after editing the schema.
A backend in another language can compile the same file with its protobuf
generator.

The Stage 2 Python worker uses the checked-in `poml_pb2.py` generated from the
same schema. Regenerate both languages together when the wire format changes.
The Python package files under `api/` and `api/proto/` make absolute imports
work when the worker is launched by path from the repository root.

Worker protocol version 1 carries explicit model and profile digests. The
worker echoes request ID, version and profile digest. Reuse of a retired
field or enum number would make older programs interpret different data as
the same message. Reserve removed numbers and bump the worker version for
incompatible changes. The private witness belongs only on the worker socket.
