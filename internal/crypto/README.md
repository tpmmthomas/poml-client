# Cryptography

[Identity signatures and VRFs](crypto.go) use Ed25519 and the RFC 9381
ECVRF-EDWARDS25519-SHA512-ELL2 library. A miner has independent inference and
encryption VRF seeds. Peers see the inference output and proof. The encryption
output and proof stay with the miner and its private worker.

[The application transcript](transcript.go) expands the public VRF with
HKDF-SHA256, using the model profile, canonical attempt bytes and slot index.
It frames the entire useful output with its length and query ID. The chain
ID, profile, parent, query and attempt authenticate each ciphertext.
[The deterministic sender](hpke.go) calls Go's RFC 9180 X25519
`DeriveKeyPair` with a seed derived from the private VRF, then applies
the Base-mode key schedule and AES-256-GCM. `OpenOutput` uses Go's
`crypto/hpke.NewRecipient` and checks the query ID before returning the output.

[Tests](vectors_test.go) cover the published VRF example, the HPKE working
group's X25519/SHA256/AES256 Base vector, a PoML ciphertext fixture calculated
independently with Python's cryptography package, and binding failures. Run
`go test ./internal/crypto`. All key bytes under `testdata` are public
reference material.

[RFC 9381 Appendix B.4](https://www.rfc-editor.org/rfc/rfc9381.html#appendix-B.4)
 · [RFC 9180](https://www.rfc-editor.org/rfc/rfc9180.html)
 · [CFRG interoperability vectors](https://github.com/cfrg/draft-irtf-cfrg-hpke/blob/master/test-vectors.json)

To regenerate the PoML vector in a separate implementation, run
`python3 internal/crypto/testdata/generate_output_vector.py` from the repository
root in a Python environment with `cryptography`. Compare its output to
`internal/crypto/testdata/poml-output.json`. The Go tests use the checked-in
vector directly.
