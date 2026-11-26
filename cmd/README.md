# Commands

`pomld` runs one node process. `pomlworker` runs the replaceable fixture worker.
`pomlctl` creates a devnet, submits signed queries, inspects tips and blocks,
and decrypts a winning ciphertext with the user key.

The commands contain little protocol logic. They call internal packages so
tests and future tools use the same validation path.

