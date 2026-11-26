# Consensus arithmetic

`ticket.go` contains the exact integer target calculation and strict unsigned
hash comparison. It avoids floating point and has boundary tests for the
paper's `C_ticket` rule.

