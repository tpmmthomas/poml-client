# Profiles

Genesis JSON contains consensus values and public keys. Node TOML contains
operator values such as addresses, data directories, worker sockets and local
logging labels. `profile` parses both and rejects incomplete key material.

Genesis also pins the worker model contract, including its digest, randomness
shape, output bound, ticket weight and certificate profile. Challenge fees,
answer windows, bond slashing and public response limits are consensus values.
`certificate_socket` can route proof requests to an independent local worker.

