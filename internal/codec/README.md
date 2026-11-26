# Codec

This package owns the consensus byte format. Transactions, blocks, tickets,
prefixes and winner statements use explicit versions, fixed-width integers and
length-prefixed fields. Every hash and signature caller uses these functions.

Changing a field requires a golden byte test and a protocol version decision.

