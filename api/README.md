# API

[The private worker protocol](proto/poml.proto) describes inference inputs,
resource limits, randomness slots, the private witness and the certificate
response. A worker can be written in any language with protobuf support.
Generated Go types sit beside the schema. Run `make proto` with the pinned
toolchain after changing a schema.

[The peer protocol](../internal/p2p/wire/peer.proto) carries public inventory
and block data between nodes. [The codec](../internal/codec/README.md)
defines the exact bytes used in block IDs and signatures. Only the local Unix
socket carries private execution and encryption witnesses.
