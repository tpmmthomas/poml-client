# Chain

`chain.Chain` validates complete blocks against parent state, stores valid
side branches and selects the local canonical tip. It applies prefix
transactions, late responses, winner settlement and height-based deadlines in
one deterministic sequence.

The chain package does not open network sockets and does not choose which
transactions a miner should freeze.

