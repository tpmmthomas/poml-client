# Store

Pebble stores canonical and side-branch blocks, state snapshots, mempool
transactions, genesis metadata and monotonic attempt counters. A block and
its resulting state are written in one durable batch before the node relays the
block.

