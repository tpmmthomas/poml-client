# Node-to-node wire schema

`peer.proto` defines the protobuf envelope and public message bodies. Every
TCP frame starts with a four-byte big-endian length. Its protobuf `kind`
selects the payload type documented beside each enum member.

Blocks and transactions travel as opaque canonical bytes inside `Data`.
The codec and ledger verify them after transport. The worker has its own local
socket and schema under `api/proto`.

Edit `peer.proto` and run `make proto` from the repository root with the
pinned compiler and Go generator on the path. `peer.pb.go` is generated.
